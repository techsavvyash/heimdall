"""Measure the scoped marker lookup against synthetic PostgreSQL history."""
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid

image = 'postgres:17.11-alpine@sha256:3540c0362c809df084ffac0ae69fe794515f9de92692f02178bcdce0ef0302ac'
name = 'heimdall-marker-scale-' + uuid.uuid4().hex

def docker(*args, **options):
    return subprocess.run(['docker', *args], check=True, **options)

def sql(statement, timeout=180):
    try:
        return docker('exec', '-i', name, 'psql', '-XAtq', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', input=statement, capture_output=True, text=True, timeout=timeout).stdout
    except subprocess.CalledProcessError as error:
        raise RuntimeError(error.stderr) from None

predicate = "FROM heimdall_retention.retirements WHERE product_id='ledger' AND environment='production' AND member_event_ids ? 'event-missing'"
query = f'SELECT count(*) FROM (SELECT 1 {predicate} LIMIT 2) matches'

def analyze(statement):
    result = json.loads(sql('EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) ' + statement))[0]
    def nodes(plan):
        yield plan
        for child in plan.get('Plans', []):
            yield from nodes(child)
    root = result['Plan']
    return {
        'nodes': [node['Node Type'] for node in nodes(root)],
        'indexes': [node['Index Name'] for node in nodes(root) if 'Index Name' in node],
        'rowsRemovedByFilter': sum(node.get('Rows Removed by Filter', 0) for node in nodes(root)),
        'sharedBlocks': root['Shared Hit Blocks'] + root['Shared Read Blocks'],
        'executionMs': result['Execution Time'],
    }

created = False
data = None
try:
    storage = []
    if bind_root := os.environ.get('HEIMDALL_TEST_POSTGRES_BIND_ROOT'):
        data = tempfile.TemporaryDirectory(prefix='heimdall-marker-scale-', dir=bind_root)
        storage = ['--mount', f'type=bind,source={data.name},target=/var/lib/postgresql/data']
    docker('run', '--detach', '--name', name, '-e', 'POSTGRES_PASSWORD', *storage, image,
           env=dict(os.environ, POSTGRES_PASSWORD=secrets.token_hex(24)), stdout=subprocess.DEVNULL)
    created = True
    for _ in range(60):
        if subprocess.run(['docker', 'exec', name, 'pg_isready', '-h', '127.0.0.1'], stdout=subprocess.DEVNULL).returncode == 0:
            break
        time.sleep(0.5)
    else:
        raise RuntimeError('PostgreSQL readiness timeout')
    migration = Path(__file__).resolve().parents[2] / 'contracts/persistence/session/0004-retirement-markers.sql'
    table = migration.read_text().split('CREATE FUNCTION', 1)[0]
    sql('CREATE SCHEMA heimdall_retention; CREATE TABLE heimdall_retention.event_identities('
        'product_id text, environment text, event_id text, PRIMARY KEY(product_id,environment,event_id));' + table)

    def seed(start, end):
        sql(f"""INSERT INTO heimdall_retention.event_identities
         SELECT p, 'production', prefix||n FROM generate_series({start},{end}) n
         CROSS JOIN (VALUES ('ledger'),('support')) products(p)
         CROSS JOIN (VALUES ('event-'),('admin-')) prefixes(prefix);
         INSERT INTO heimdall_retention.retirements
         SELECT p, 'production', 'retire-'||n, 'event-'||n, 'tenant', 100000, 1000,
          'admin-'||n, jsonb_build_array('event-'||n,'terminal-'||n)
         FROM generate_series({start},{end}) n CROSS JOIN (VALUES ('ledger'),('support')) products(p);""")
    seed(1, 1000)
    sql('ANALYZE heimdall_retention.retirements;')
    baseline_small = analyze(query)
    seed(1001, 100000)
    sql('ANALYZE heimdall_retention.retirements;')
    baseline_large = analyze(query)
    for start in range(100001, 500001, 100000):
        seed(start, start + 99999)
    build_started = time.monotonic()
    sql("SET statement_timeout='20s'; " + migration.with_name('v5-retirement-member-index.sql').read_text())
    build_seconds = time.monotonic() - build_started
    sql('ANALYZE heimdall_retention.retirements;')
    indexed_miss = analyze(query)
    hit_query = query.replace('event-missing', 'terminal-100000')
    indexed_hit = analyze(hit_query)
    scoped_count = sql(hit_query).strip()
    sql("INSERT INTO heimdall_retention.event_identities VALUES ('ledger','production','event-1-conflict'); INSERT INTO heimdall_retention.retirements VALUES ('ledger','production','conflicting','event-1-conflict','tenant',100000,1000,'admin-1','[\"terminal-100000\"]');")
    duplicate_count = sql(hit_query).strip()
    if baseline_large['rowsRemovedByFilter'] < 100000 or indexed_miss['sharedBlocks'] >= baseline_large['sharedBlocks'] or scoped_count != '1' or duplicate_count != '2':
        raise RuntimeError('unexpected query plan or scoped marker count')
    sql("""ALTER TABLE heimdall_retention.retirements SET (autovacuum_enabled=false);
        INSERT INTO heimdall_retention.event_identities
        SELECT 'collision-'||n, 'production', id FROM generate_series(1,128) n
        CROSS JOIN (VALUES ('collision-root'),('collision-admin')) keys(id);
        INSERT INTO heimdall_retention.retirements
        SELECT 'collision-'||n,'production','collision','collision-root','tenant',100000,1000,
               'collision-admin','["collision-root","terminal-100000"]'
        FROM generate_series(1,128) n;""")

    def write_markers():
        for start in range(500001, 510001, 500):
            seed(start, start + 499)

    scoped_query = hit_query.replace('LIMIT 2', "AND retirement_id<>'conflicting' LIMIT 2")
    with ThreadPoolExecutor(max_workers=1) as pool:
        writer = pool.submit(write_markers)
        read_plans = []
        while not writer.done() and len(read_plans) < 100:
            read_plans.append(analyze(scoped_query))
            if sql(scoped_query).strip() != '1':
                raise RuntimeError('scoped lookup changed during writes')
        writer.result()
    if not read_plans:
        raise RuntimeError('no reads ran while the writer was active')

    fresh_query = hit_query.replace('terminal-100000', 'terminal-510000')
    pending_hit = analyze(fresh_query)
    if sql(fresh_query).strip() != '1' or sql(query).strip() != '0' or sql(hit_query).strip() != '2':
        raise RuntimeError('marker lookup changed across writes')
    flushed_pages = int(sql("SELECT gin_clean_pending_list('heimdall_retention.retirement_member_lookup')"))
    cleaned_hit = analyze(fresh_query)
    if (flushed_pages < 1 or sql(fresh_query).strip() != '1'
            or any('retirement_member_lookup' not in plan['indexes']
                   for plan in [pending_hit, cleaned_hit, *read_plans])):
        raise RuntimeError('pending-list visibility or cleanup was not exercised')
    print(json.dumps(dict(run=name, postgresImage=image, baseline1000=baseline_small, baseline100000=baseline_large,
                          indexedMiss500000=indexed_miss, indexedHit500000=indexed_hit,
                          scopedCount=int(scoped_count), duplicateCount=int(duplicate_count),
                          buildSeconds=round(build_seconds, 3), buildMarkers=1000000,
                          insertedMarkers=20000, collisionProducts=128,
                          concurrentReads=len(read_plans),
                          concurrentMaxSharedBlocks=max(plan['sharedBlocks'] for plan in read_plans),
                          pendingHit=pending_hit, flushedPages=flushed_pages, cleanedHit=cleaned_hit), indent=2))
finally:
    if created:
        docker('rm', '--force', '--volumes', name, stdout=subprocess.DEVNULL)
    if data:
        data.cleanup()
