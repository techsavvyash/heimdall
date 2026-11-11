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
application = 'heimdall-events-'+RUN
observations = {}
def release():
    global holder
    # Only the exact application name owned by this run can be terminated.
    sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='"+application+"'")
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
    def events():
        return api('/admin/v1/events/_search', {'resourceOwner':org, 'asc':True, 'limit':1000})['events']
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
finally:
    if holder: release()
    if org: api('/v2/organizations/'+org, method='DELETE')
print(json.dumps({'suite':'railway-event-visibility-v1','runId':RUN,'observations':observations,
    'cleanup':'owned database sessions terminated and organization deleted',
    'limitations':['synthetic writer advisory lock; no event table modifications',
                   'not a proof against database clock reversal, retention gaps or all concurrent writer races']}, indent=2))
