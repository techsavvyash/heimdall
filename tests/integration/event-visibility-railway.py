"""Read-only database lock fault against the dedicated verification stack; never production."""
import concurrent.futures
import json
import os
import pathlib
import shlex
import subprocess
import sys
import time
import urllib.request
import uuid

assert __debug__ and sys.argv[1:] == ['--execute-verification-event-visibility']
PROJECT = '944fc9be-8648-4237-82bc-72e4bd24a4c9'
ENVIRONMENT = '7bc7c286-b2dc-4415-85eb-90dd554ab2fb'
PG = '648133ac-305b-495d-a77e-4d0c49103feb'
BASE = 'https://capability-zitadel-verification.up.railway.app'
PAT = os.environ['HEIMDALL_PROBE_ZITADEL_PAT']
KEY = str(pathlib.Path.home()/'.ssh/heimdall-verification')
RUN = str(uuid.uuid4())
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None

def api(path, body=None, method='POST'):
    req = urllib.request.Request(BASE+path, data=None if body is None else json.dumps(body).encode(),
        headers={'Authorization': 'Bearer '+PAT, 'Content-Type': 'application/json'}, method=method)
    with urllib.request.build_opener(NoRedirect).open(req, timeout=40) as response:
        raw = response.read()
        return json.loads(raw) if raw else None

def sql_args(query):
    command = 'psql -U heimdall_probe -d zitadel -v ON_ERROR_STOP=1 -Atc '+shlex.quote(query)
    return ['railway', 'ssh', '--service', PG, '--environment', ENVIRONMENT, '--project', PROJECT,
            '--identity-file', KEY, '--', command]

def sql(query):
    result = subprocess.run(sql_args(query), capture_output=True, text=True, timeout=30)
    assert result.returncode == 0, 'Verification database command failed'
    return result.stdout.strip()

def wait(predicate):
    end = time.monotonic()+20
    while time.monotonic() < end:
        value = predicate()
        if value: return value
        time.sleep(.25)
    raise AssertionError('Observation deadline; existing fault must be cleaned up')

org = None
holder = None
late_writer = None
application = 'heimdall-events-'+RUN
late_application = 'heimdall-late-events-'+RUN
observations = {}
def terminate_owned(application_name):
    sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='"+application_name+"'")

def release():
    global holder
    # Only the exact application name owned by this run can be terminated.
    terminate_owned(application)
    if holder:
        holder.wait(timeout=15)
        holder = None
try:
    org = api('/v2/organizations', {'name': 'heimdall-visibility-'+RUN})['organizationId']
    assert org.isdigit()
    instance = sql("SELECT DISTINCT instance_id FROM eventstore.events2 WHERE aggregate_id='"+org+"'")
    assert instance.isdigit()
    def start_holder(lock):
        global holder
        query = "SET application_name='"+application+"'; BEGIN; "+lock+" SELECT pg_sleep(90); ROLLBACK;"
        holder = subprocess.Popen(sql_args(query), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        wait(lambda: sql("SELECT count(*) FROM pg_stat_activity WHERE application_name='"+application+"' AND wait_event='PgSleep'") == '1')
    def events(window=None):
        query = {'resourceOwner':org, 'asc':True, 'limit':1000}
        if window is not None:
            query['range'] = window
        return api('/admin/v1/events/_search', query).get('events', [])
    start_holder('')
    assert events()
    observations['unrelatedOpenTransactionReadCompleted'] = True
    release()
    user = api('/v2/users/new', {'organizationId':org, 'username':'visibility-'+RUN,
                              'machine':{'name':'Disposable visibility marker'}})['id']
    start_holder("SELECT pg_advisory_xact_lock_shared('eventstore.events2'::REGCLASS::OID::INTEGER, hashtext('"+instance+"'));")
    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
        pending = pool.submit(events)
        try:
            wait(lambda: int(sql("SELECT count(*) FROM pg_stat_activity WHERE datname='zitadel' AND wait_event='advisory' AND query LIKE 'select pg_advisory_lock(%'")) > 0)
            assert not pending.done(), 'Event reader must wait for the instance writer barrier'
            observations['readerObservedWaitingForWriterBarrier'] = True
        finally:
            release()
        result = pending.result(timeout=40)
    assert any(event['aggregate']['id'] == user for event in result)
    observations['committedMarkerVisibleAfterDrain'] = True
    observations['returnedEvents'] = len(result)
    # Exercise a transaction that began before the cutoff but reaches the instance
    # writer lock only after the event reader finished. No event rows are inserted.
    gate = f"hashtextextended('{RUN}', 0)"
    start_holder(f"SELECT pg_advisory_xact_lock({gate});")
    query = (
        "SET application_name='"+late_application+"'; BEGIN; "
        f"SELECT pg_advisory_xact_lock({gate}); "
        "SELECT pg_advisory_xact_lock_shared('eventstore.events2'::REGCLASS::OID::INTEGER, hashtext('"+instance+"')); "
        "SELECT json_build_object('transactionMicros', (EXTRACT(EPOCH FROM now()) * 1000000)::bigint, "
        "'afterLockMicros', (EXTRACT(EPOCH FROM clock_timestamp()) * 1000000)::bigint); ROLLBACK;"
    )
    late_writer = subprocess.Popen(sql_args(query), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    waiting_sql = (
        "SELECT count(*) FROM pg_stat_activity WHERE application_name='"+late_application+"' "
        "AND wait_event_type='Lock' AND wait_event='advisory'"
    )
    wait(lambda: sql(waiting_sql) == '1')
    cutoff = json.loads(sql("""
        SELECT json_build_object(
            'micros', (EXTRACT(EPOCH FROM cutoff) * 1000000)::bigint,
            'since', to_char((cutoff - INTERVAL '1 hour') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
            'until', to_char(cutoff AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
        ) FROM (SELECT clock_timestamp() AS cutoff) sample
    """))
    assert any(event['aggregate']['id'] == user for event in events({
        'since': cutoff['since'], 'until': cutoff['until'],
    }))
    assert sql(waiting_sql) == '1', 'The reader must return before the synthetic writer acquires its lock'
    release()
    output, _ = late_writer.communicate(timeout=15)
    assert late_writer.returncode == 0, 'Synthetic writer did not finish'
    stamps = json.loads(next(line for line in output.splitlines() if line.startswith('{')))
    late_writer = None
    assert stamps['transactionMicros'] < cutoff['micros'] < stamps['afterLockMicros']
    # Inspect the installed mapping because later migrations can replace the initial function.
    assert sql("""
        SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
        WHERE n.nspname='eventstore' AND p.proname='commands_to_events'
          AND p.prosrc ILIKE '%statement_timestamp() AS created_at%'
          AND p.prosrc ILIKE '%EXTRACT(EPOCH FROM clock_timestamp()) AS position%'
    """) == '1'
    observations['readerFinishedBeforePreCutoffTransactionAcquiredWriterLock'] = True
    observations['writerTransactionTimestampPredatesCutoffAfterTakingWriterLock'] = True
    observations['installedEventMappingUsesStatementTimeAndWallClockPosition'] = True
    late_user = api('/v2/users/new', {'organizationId':org, 'username':'visibility-late-'+RUN,
                                    'machine':{'name':'Disposable post-cutoff marker'}})['id']
    assert not any(event['aggregate']['id'] == late_user for event in events({
        'since': cutoff['since'], 'until': cutoff['until'],
    }))
    assert any(event['aggregate']['id'] == late_user for event in events())
    observations['postCutoffProviderWriteExcludedFromHistoricalWindow'] = True
    observations['databaseDefaultTransactionTimeout'] = sql('SHOW transaction_timeout')


finally:
    if late_writer:
        terminate_owned(late_application)
        late_writer.communicate(timeout=15)
    if holder: release()
    if org: api('/v2/organizations/'+org, method='DELETE')
print(json.dumps({'suite':'railway-event-visibility-v2','runId':RUN,'observations':observations,
    'cleanup':'owned database sessions terminated and organization deleted',
    'limitations':['synthetic transaction follows lock and timestamp ordering; no direct event table writes',
                   'does not measure maximum writer transaction duration, clock skew or effective audit retention']}, indent=2))
