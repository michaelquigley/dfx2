import ctypes, subprocess, os, time, sys, json
from pathlib import Path
ctypes.CDLL(None).prctl(36, 1, 0, 0, 0)  # child subreaper, this supervisor only
name, *command = sys.argv[1:]
folder=Path('/tmp/dfx2-webkit-exit-repro');folder.mkdir(exist_ok=True)
with (folder/(name+'.log')).open('w') as log:
 p=subprocess.Popen(command,stdin=subprocess.DEVNULL,stdout=log,stderr=subprocess.STDOUT)
 code=p.wait(timeout=20)
 results=[{'host':p.pid,'exit':code}]
 deadline=time.monotonic()+15
 while True:
  try: pid,status=os.waitpid(-1,os.WNOHANG)
  except ChildProcessError: break
  if pid:
   results.append({'child':pid,'exit':os.waitstatus_to_exitcode(status)})
  elif time.monotonic()>deadline:
   results.append({'timeout':'children still alive'});break
  else:time.sleep(.05)
text=json.dumps(results)
(folder/(name+'-exit.json')).write_text(text+'\n')
print(name,text)
print((folder/(name+'.log')).read_text()[-700:])
