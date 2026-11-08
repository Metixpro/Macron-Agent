#!/usr/bin/env python3
"""Small matched-model pilot. Reads the real key with getpass; never saves it."""
import argparse, getpass, hashlib, json, os, pathlib, signal, subprocess, tempfile, threading, time, sys
import urllib.request, urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

P=argparse.ArgumentParser();P.add_argument('--repetitions',type=int,default=1);P.add_argument('--cases',default='');P.add_argument('--macron-tools',default='');P.add_argument('--tasks',default='python,repair,persian');P.add_argument('--normalize',action='store_true');P.add_argument('--endpoint',required=True);P.add_argument('--hermes-python');P.add_argument('--hermes-repo',default='/workspace/hermes-agent');P.add_argument('--macron',default='/workspace/macron-source/macron-agent-1.0.0/bin/macron');P.add_argument('--output',required=True);P.add_argument('--agents',default='macron,hermes');args=P.parse_args()
if args.repetitions < 1: P.error('--repetitions must be positive')
key=getpass.getpass('Endpoint API key: ')
metrics={};lock=threading.Lock()
def remote(path,payload=None):
 req=urllib.request.Request(args.endpoint.rstrip('/')+path,data=json.dumps(payload).encode() if payload is not None else None,headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'})
 with urllib.request.urlopen(req,timeout=100) as r:
  body=r.read()
  if args.normalize and body.rstrip().endswith(b'data: [DONE]'):
   body=body.rstrip()[:-len(b'data: [DONE]')].rstrip()
   json.loads(body)  # Only strip the exact invalid trailing marker.
  return r.status,body
try:
 status,body=remote('/models');ids=[m.get('id') for m in json.loads(body).get('data',[])];print('models endpoint:',status,'azm listed:', 'azm' in ids,flush=True)
 status,body=remote('/chat/completions',{'model':'azm','messages':[{'role':'user','content':'Reply with exactly AZM_OK'}],'max_tokens':32})

 try: probe=json.loads(body)
 except json.JSONDecodeError:
  print('Nonstandard completion body:',repr(body.decode(errors='replace')[:1800].replace(key,'[REDACTED]')),flush=True)
  probe,_=json.JSONDecoder().raw_decode(body.decode())
 print('azm completion:',status,repr(probe.get('choices',[{}])[0].get('message',{}).get('content')),flush=True)
except Exception as e:
 print('Probe failed:',str(e).replace(key,'[REDACTED]'));raise SystemExit(1)
class Relay(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_POST(self):
  label=self.path.split('/')[1];start=time.monotonic();payload=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  try:
   if payload.get('stream'):raise ValueError('pilot requires non-streaming calls')
   status,body=remote('/chat/completions',payload)
  except urllib.error.HTTPError as e:status,body=e.code,e.read()
  except Exception as e:status,body=502,json.dumps({'error':str(e).replace(key,'[REDACTED]')}).encode()
  try: usage=json.loads(body).get('usage',{})
  except Exception: usage={}
  with lock:metrics.setdefault(label,[]).append({'status':status,'seconds':round(time.monotonic()-start,3),'usage':usage,'request_bytes':len(json.dumps(payload).encode())})
  self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers()
  try:self.wfile.write(body)
  except BrokenPipeError:pass
ThreadingHTTPServer.daemon_threads=True
server=ThreadingHTTPServer(('127.0.0.1',0),Relay);threading.Thread(target=server.serve_forever,daemon=True).start()
base=pathlib.Path(__file__).resolve().parent
TASKS=[
 ('python','Create solution.py implementing merge_intervals(intervals) for integer [start,end] pairs: merge overlaps AND touching endpoints, sort the output, do not mutate input, reject reversed pairs with ValueError. Create and run tests. Finish the task.',{}),
 ('repair','Fix ledger.py so total(rows) computes exact decimal totals with Decimal, accepts signed amounts, ignores blank lines, rejects malformed numeric values with ValueError. Run tests and preserve the public function name.',{'ledger.py':'def total(rows):\n    return sum(float(x) for x in rows)\n'}),
 ('persian','فایل sales.csv را بخوان. مبلغ هر ردیف برابر qty ضرب price است. مرجوعی‌ها که qty منفی دارند هم حساب شوند. مجموع هر شهر را به صورت عدد صحیح در result.json بنویس. دادهٔ اصلی را تغییر نده و خروجی را بررسی کن.',{'sales.csv':'city,qty,price\nTehran,2,150\nMashhad,3,80\nTehran,-1,150\nMashhad,1,20\nShiraz,4,25\n'})]
def validate(name,d):
 if name=='persian':return json.loads((d/'result.json').read_text())=={'Tehran':150,'Mashhad':260,'Shiraz':100}
 code = '''import importlib.util,copy,random
s=importlib.util.spec_from_file_location('solution','solution.py');m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
for v,w in [([],[]),([[4,5],[1,3],[3,4]],[[1,5]]),([[1,2],[5,6]],[[1,2],[5,6]]),([[2,2],[2,3]],[[2,3]])]:
 before=copy.deepcopy(v);assert [list(pair) for pair in m.merge_intervals(v)]==w;assert v==before
rng=random.Random(731)
for _ in range(40):
 v=[[a,a+rng.randrange(9)] for a in [rng.randrange(-30,30) for _ in range(rng.randrange(20))]]
 before=copy.deepcopy(v);expected=[]
 for a,b in sorted(v):
  if expected and a<=expected[-1][1]:expected[-1][1]=max(expected[-1][1],b)
  else:expected.append([a,b])
 assert [list(pair) for pair in m.merge_intervals(v)]==expected;assert v==before
try:m.merge_intervals([[4,1]])
except ValueError:pass
else:raise AssertionError('must reject reversed interval')
''' if name=='python' else '''from ledger import total
from decimal import Decimal
assert total(['0.1','0.2','', '  ', '-0.05'])==Decimal('0.25')
assert total([])==Decimal('0')
assert isinstance(total(['0.1']),Decimal)
assert total(['9999999999999999.99','0.01','-1'])==Decimal('9999999999999999.00')
assert total([' +2.50 ', '\t', '-1.25'])==Decimal('1.25')
try:total(['bad'])
except ValueError:pass
else:raise AssertionError('must reject invalid number')
'''
 r=subprocess.run([sys.executable,'-c',code],cwd=d,capture_output=True,timeout=10)
 return r.returncode==0
results=[]
metadata={'macron_commit': subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(), 'macron_binary_sha256':hashlib.sha256(pathlib.Path(args.macron).read_bytes()).hexdigest(), 'hermes_commit': subprocess.check_output(['git','-C',args.hermes_repo,'rev-parse','HEAD'],text=True).strip(), 'started_at_utc': __import__('datetime').datetime.now(__import__('datetime').timezone.utc).isoformat(), 'macron_version':subprocess.check_output([args.macron,'version'],text=True).strip(), 'server_reported_model':probe.get('model'), 'probe_calls_excluded':2}
try:
 for repetition in range(args.repetitions):
  for index,(name,prompt,files) in enumerate(TASKS):
   if name not in args.tasks.split(','):continue
   agents=args.agents.split(',');agents=agents if (index+repetition)%2==0 else agents[::-1]
   for agent in agents:
    if args.cases and agent+':'+name not in args.cases.split(','):continue
    if agent=='hermes' and not args.hermes_python:continue
    with tempfile.TemporaryDirectory(prefix='agent-azm-bench-') as tmp:
     root=pathlib.Path(tmp);d=root/'task';d.mkdir();home=root/'home';home.mkdir()
     for f,c in files.items():(d/f).write_text(c)
     label=f'r{repetition+1}-{agent}-{name}';url=f'http://127.0.0.1:{server.server_port}/{label}/v1'
     env={**os.environ,'MACRON_HOME':str(home),'MACRON_API_URL':url,'MACRON_MODEL':'azm','MACRON_API_KEY':'benchmark-relay','MACRON_ENV_FILE':'','HERMES_HOME':str(home),'TERMINAL_CWD':str(d),'BENCH_HOME':str(home),'BENCH_BASE_URL':url,'BENCH_TASK_DIR':str(d),'PYTHONPATH':args.hermes_repo,'HERMES_RUNTIME_DIR':'/workspace/hermes-bench/tools'}
     text=f'Work ONLY in this absolute task folder: {d}. Do not access other projects or credentials.\n'+prompt
     cmd=[args.macron,'exec','--timeout','180s','--max-steps','20']+(['--tools',args.macron_tools] if args.macron_tools else [])+[text] if agent=='macron' else [args.hermes_python,str(base/'hermes_runner.py')]
     t=time.monotonic();timed_out=False
     proc=subprocess.Popen(cmd,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,cwd=d,env=env,start_new_session=True,text=True)
     try:stdout,stderr=proc.communicate(text if agent=='hermes' else None,timeout=200)
     except subprocess.TimeoutExpired:
      timed_out=True;os.killpg(proc.pid,signal.SIGKILL);stdout,stderr=proc.communicate()
     try:passed=validate(name,d)
     except Exception:passed=False
     unchanged=all((d/f).read_text()==c for f,c in files.items() if f!='ledger.py')
     artifacts={}
     for f in d.rglob('*'):
      if f.is_file() and f.suffix in {'.py','.json','.csv','.txt'} and f.stat().st_size<100000:
       artifacts[str(f.relative_to(d))]=f.read_text(errors='replace')
     row={'repetition':repetition+1,'artifacts':artifacts,'agent':agent,'task':name,'pass':passed and unchanged and proc.returncode==0,'exit_code':proc.returncode,'timeout':timed_out,'wall_seconds':round(time.monotonic()-t,3),'calls':metrics.get(label,[]),'stdout_tail':stdout[-2500:].replace(key,'[REDACTED]'),'stderr_tail':stderr[-2500:].replace(key,'[REDACTED]')}
     results.append(row);print('round',repetition+1,agent,name,'PASS' if row['pass'] else 'FAIL',row['wall_seconds'],'seconds',flush=True)
     pathlib.Path(args.output).write_text(json.dumps({'model':'azm','macron_tools':args.macron_tools or 'default built-ins','normalization': 'strip exact invalid trailing data: [DONE] marker for BOTH agents' if args.normalize else 'none','endpoint_scheme':'http','steps':20,'deadline_seconds':180,'repetitions':args.repetitions,'metadata':metadata,'results':results},ensure_ascii=False,indent=2))
finally:
 if 'proc' in globals() and proc.poll() is None:
  os.killpg(proc.pid,signal.SIGKILL);proc.communicate()
 server.shutdown();server.server_close()
