"""Explicit maintenance fault probe for Heimdall's dedicated Railway verification stack.

Requires Railway CLI authentication, the registered SSH key, and old/new API keys
in environment variables. Never run against a product deployment. No SDK runtime.
"""
import concurrent.futures
import http.client
import http.server
import json
import os
import pathlib
import shlex
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid

if not __debug__:
    raise RuntimeError('Run without Python optimization; verification assertions are required')

PROJECT = '944fc9be-8648-4237-82bc-72e4bd24a4c9'
ENVIRONMENT = '7bc7c286-b2dc-4415-85eb-90dd554ab2fb'
FGA = '3db1d84b-b58d-47d5-8cbb-6c9d5c766914'
PG = '648133ac-305b-495d-a77e-4d0c49103feb'
BASE = 'https://capability-openfga-verification.up.railway.app'
OLD = os.environ['HEIMDALL_PROBE_OPENFGA_KEY']
NEW = os.environ['HEIMDALL_PROBE_NEW_OPENFGA_KEY']
assert OLD and NEW and OLD != NEW
assert sys.argv[1:] == ['--execute-verification-recovery']
SSH_KEY = os.environ.get('HEIMDALL_PROBE_SSH_KEY', str(pathlib.Path.home()/'.ssh/heimdall-verification'))
RUN = str(uuid.uuid4())
observations = {}

def cli(args, data=None):
    r = subprocess.run(['railway', *args], input=data, capture_output=True, text=True, timeout=180)
    if r.returncode:
        raise RuntimeError('Railway command failed: ' + r.stderr.replace(OLD, '[REDACTED]').replace(NEW, '[REDACTED]'))
    return r.stdout

def deployments():
    return json.loads(cli(['deployment', 'list', '--service', FGA, '--environment', ENVIRONMENT, '--project', PROJECT, '--json']))

def sql_args(query):
    command = 'psql -U heimdall_probe -d openfga -v ON_ERROR_STOP=1 -Atc ' + shlex.quote(query)
    return ['ssh', '--service', PG, '--environment', ENVIRONMENT, '--project', PROJECT, '--identity-file', SSH_KEY, '--', command]

def sql(query):
    return cli(sql_args(query)).strip()

def request(path, body=None, key=None, method=None):
    req = urllib.request.Request(BASE+path, data=None if body is None else json.dumps(body).encode(),
        headers={'Authorization':'Bearer '+(key or active_key), 'Content-Type':'application/json'}, method=method)
    # The fixed test endpoint must never redirect credentials elsewhere.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    try:
        with urllib.request.build_opener(NoRedirect).open(req, timeout=20) as r:
            raw = r.read(); return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        return e.code, None

def ok(path, body=None, method=None):
    status, result = request(path, body, method=method)
    assert 200 <= status < 300, f'Unexpected HTTP {status}'
    return result

def wait(predicate, timeout=120):
    end = time.monotonic()+timeout
    while time.monotonic() < end:
        result = predicate()
        if result: return result
        time.sleep(1)
    raise RuntimeError('Observation deadline; inspect existing deployment before any retry')

def deploy():
    result = json.loads(cli(['api', f'mutation {{serviceInstanceDeployV2(serviceId:"{FGA}",environmentId:"{ENVIRONMENT}")}}', '--compact']))
    assert not result.get('errors'), 'Deployment mutation failed'
    return result

def healthy():
    try: return request('/healthz')[0] == 200
    except (urllib.error.URLError, TimeoutError): return False

active_key = OLD
store = None
lock = None
stopped = False
rotated = False
restarted = False
try:
    current = deployments()[0]
    assert current['status'] == 'SUCCESS'
    config = current['meta']['serviceManifest']['deploy']
    assert config['numReplicas'] == 1
    assert sum(x['numReplicas'] for x in config['multiRegionConfig'].values()) == 1
    variables = json.loads(cli(['variables', '--service', FGA, '--environment', ENVIRONMENT, '--project', PROJECT, '--json']))
    assert variables['OPENFGA_AUTHN_METHOD'] == 'preshared'
    assert variables['OPENFGA_AUTHN_PRESHARED_KEYS'] == OLD
    observations['oldDeployment'] = current['id']
    store = ok('/stores', {'name':'heimdall-recovery-'+RUN})['id']
    model = ok(f'/stores/{store}/authorization-models', {'schema_version':'1.1','type_definitions':[
        {'type':'user'}, {'type':'workspace','relations':{'member':{'this':{}}},
         'metadata':{'relations':{'member':{'directly_related_user_types':[{'type':'user'}]}}}}]})['authorization_model_id']
    committed = {'user':'user:maya','relation':'member','object':'workspace:committed'}
    blocked = dict(committed, object='workspace:blocked')
    def write(t): return {'authorization_model_id':model,'writes':{'tuple_keys':[t]}}
    def read(t): return ok(f'/stores/{store}/read', {'tuple_key':t,'consistency':'HIGHER_CONSISTENCY'})['tuples']
    class DropResponse(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args): pass
        def do_POST(self):
            try:
                ok(f'/stores/{store}/write', write(committed))
                observations['proxyObservedUpstreamCommit'] = True
            finally:
                self.connection.shutdown(socket.SHUT_RDWR)
                self.connection.close()
    proxy = http.server.HTTPServer(('127.0.0.1',0), DropResponse)
    thread = threading.Thread(target=proxy.handle_request, daemon=True); thread.start()
    try:
        urllib.request.urlopen(urllib.request.Request(f'http://127.0.0.1:{proxy.server_port}/',method='POST'), timeout=30)
        raise AssertionError('Fault client received response')
    except (urllib.error.URLError, http.client.RemoteDisconnected, ConnectionError):
        observations['clientLostCommittedResponse'] = True
    finally:
        thread.join(timeout=30); proxy.server_close()
    assert observations.get('proxyObservedUpstreamCommit') and len(read(committed)) == 1
    lock = subprocess.Popen(['railway', *sql_args('BEGIN; LOCK TABLE tuple IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(300); ROLLBACK;')], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    lock_pid = wait(lambda: sql("SELECT pid FROM pg_locks WHERE relation='tuple'::regclass AND mode='AccessExclusiveLock' AND granted"))
    assert lock_pid.isdigit()
    def attempt():
        try: return {'status':request(f'/stores/{store}/write',write(blocked))[0]}
        except (urllib.error.URLError, TimeoutError, ConnectionError): return {'transportFailure':True}
    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
        pending = pool.submit(attempt)
        wait(lambda: int(sql(f"SELECT count(*) FROM pg_stat_activity WHERE datname='openfga' AND pid<>{lock_pid} AND wait_event_type='Lock'")) > 0, 30)
        observations['writeObservedWaitingInDatabase'] = True
        print('Database wait observed; removing verification OpenFGA deployment', file=sys.stderr, flush=True)
        cli(['down','--service',FGA,'--environment',ENVIRONMENT,'--project',PROJECT,'--yes'])
        stopped = True
        wait(lambda: next(d for d in deployments() if d['id']==current['id'])['status']=='REMOVED')
        assert all(d['status'] not in ['SUCCESS','DEPLOYING','BUILDING','QUEUED','INITIALIZING','WAITING'] for d in deployments())
        observations['oldDeploymentRemoved'] = True
        sql(f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='openfga' AND pid<>pg_backend_pid() AND pid<>{lock_pid}")
        assert sql(f"SELECT count(*) FROM pg_stat_activity WHERE datname='openfga' AND pid<>pg_backend_pid() AND pid<>{lock_pid}") == '0'
        observations['databaseBackendsDrained'] = True
        sql(f'SELECT pg_terminate_backend({lock_pid})'); lock.wait(timeout=30); lock=None
        observations['interruptedClientOutcome'] = pending.result(timeout=30)
        assert observations['interruptedClientOutcome'].get('status',500) >= 400
    cli(['variables','--service',FGA,'--environment',ENVIRONMENT,'--project',PROJECT,'--set-from-stdin','OPENFGA_AUTHN_PRESHARED_KEYS','--skip-deploys'], NEW)
    rotated = True; active_key = NEW
    deploy(); restarted = True
    wait(lambda: deployments()[0]['status']=='SUCCESS',180); wait(healthy)
    observations['newDeployment'] = deployments()[0]['id']
    assert observations['newDeployment'] != current['id']
    assert request(f'/stores/{store}/write',write(blocked),OLD)[0] == 401
    assert len(read(blocked)) == 0 and len(read(committed)) == 1
    observations['interruptedAbsentCommittedPresent'] = True
    ok(f'/stores/{store}/write', {'authorization_model_id':model,'deletes':{'tuple_keys':[committed]}})
    assert request(f'/stores/{store}/write',write(committed),OLD)[0] == 401
    assert len(read(committed)) == 0
    observations['staleCredentialCannotRegrant'] = True
    ok(f'/stores/{store}/write',write(blocked)); assert len(read(blocked))==1
    ok(f'/stores/{store}/write', {'authorization_model_id':model,'deletes':{'tuple_keys':[blocked]}})
    assert len(read(blocked))==0
    observations['newCredentialReconcilePassed'] = True
finally:
    # Keep the service available after a fault; never restore retired credentials.
    if lock is not None:
        sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='openfga' AND query LIKE 'BEGIN; LOCK TABLE tuple%' AND pid<>pg_backend_pid()")
        lock.wait(timeout=30)
    if stopped and not restarted:
        if rotated: active_key=NEW
        deploy(); wait(lambda: deployments()[0]['status']=='SUCCESS',180); wait(healthy)
    if store is not None:
        ok(f'/stores/{store}',method='DELETE'); observations['ownedStoreDeleted']=True
print(json.dumps({'suite':'openfga-quiescence-railway-v1','runId':RUN,'observedAt':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'observations':observations,
    'limitations':['one replica and sole preshared credential in dedicated verification environment','deployment-wide maintenance, not per-workspace fencing','no production writers, autoscaling or independent credential brokers','SDK journal, quarantine and authorized recovery integration remain unimplemented']},indent=2))
