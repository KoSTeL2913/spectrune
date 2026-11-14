import os,subprocess,time,tempfile,sys,signal,shutil,errno
binary=sys.argv[1]; baseline='--baseline' in sys.argv
children=[]
with tempfile.TemporaryDirectory(prefix='spectrune-single-config-') as config:
 env=os.environ.copy();env['XDG_CONFIG_HOME']=config;env['XDG_CACHE_HOME']=config+'/cache';env['GIO_USE_VFS']='local';env['NO_AT_BRIDGE']='1';env['GSETTINGS_BACKEND']='memory'
 output=tempfile.TemporaryFile()
 def run(*a):return subprocess.run(a,capture_output=True,text=True)
 def windows(visible=False):
  args=['xdotool','search']+(['--onlyvisible'] if visible else [])+['--name','^Spectrune$']
  r=run(*args);result=[]
  for window in r.stdout.split():
   geometry=run('xdotool','getwindowgeometry','--shell',window).stdout
   values=dict(line.split('=',1) for line in geometry.splitlines() if '=' in line)
   # GTK's group leader and tray plug can share the application title.
   if int(values.get('WIDTH','0'))>=300 and int(values.get('HEIGHT','0'))>=200:result.append(window)
  return result
 def wait(fn,label,seconds=15):
  end=time.monotonic()+seconds
  while time.monotonic()<end:
   if fn():return
   time.sleep(.1)
  raise RuntimeError(label)
 def launch(argument="/gui"):
  p=subprocess.Popen([binary,argument],env=env,stdout=output,stderr=output,start_new_session=True);children.append(p);return p
 def repeat(argument="/gui"):
  p=launch(argument);assert p.wait(timeout=12)==0
 try:
  wm=subprocess.Popen(['metacity','--sm-disable','--replace'],env=env,stdout=output,stderr=output,start_new_session=True);children.append(wm)
  wait(lambda:run('wmctrl','-m').returncode==0,'window manager missing')
  first=launch();wait(lambda:len(windows(True))==1,'first window missing');original=windows(True)[0]
  if baseline:
   second=launch();wait(lambda:len(windows(True))==2,'baseline did not create second window')
   print('REPRODUCED Linux 2.0.71.0: second launch created two windows',windows(True),flush=True);sys.exit(0)
  repeat();assert windows(True)==[original];assert first.poll() is None
  print('PASS repeated launch keeps original PID/window',first.pid,original,flush=True)
  burst=[launch() for _ in range(6)]
  for p in burst:assert p.wait(timeout=12)==0
  assert windows(True)==[original]
  print('PASS six repeated launches create no additional windows',flush=True)
  subprocess.run(['xdotool','windowminimize',original],check=True)
  wait(lambda:'Iconic' in run('xprop','-id',original,'WM_STATE').stdout,'window did not minimize')
  repeat();wait(lambda:windows(True)==[original] and 'Normal' in run('xprop','-id',original,'WM_STATE').stdout,'existing minimized window not restored')
  wait(lambda:run('xdotool','getactivewindow').stdout.strip()==original,'original window not foreground')
  print('PASS minimized original window restored and focused',flush=True)
  subprocess.run(['wmctrl','-ic',hex(int(original))],check=True)
  wait(lambda:not windows(True),'window did not hide to tray')
  assert first.poll() is None
  repeat();wait(lambda:windows(True)==[original],'tray-hidden original not restored')
  print('PASS tray-hidden original window restored with same PID/window',flush=True)
  repeat('/gui-restart');assert windows(True)==[original]
  print('PASS restart command reuses an existing live window',flush=True)
  restarting=launch('/gui-restart');time.sleep(.25);first.terminate();first.wait(timeout=10)
  wait(lambda:len(windows(True))==1 and run('xdotool','getwindowpid',windows(True)[0]).stdout.strip()==str(restarting.pid),'restart did not acquire ownership after old process exited')
  first=restarting;original=windows(True)[0];repeat();assert windows(True)==[original]
  print('PASS restart waits for old owner and becomes the single replacement',flush=True)
  first.terminate();first.wait(timeout=10);wait(lambda:not windows(),'first window did not exit')
  raced=[launch() for _ in range(6)]
  wait(lambda:len(windows(True))==1 and sum(p.poll() is None for p in raced)==1,'simultaneous first launches did not converge')
  print('PASS six simultaneous first launches converge to one GUI',flush=True)
 finally:
  for p in reversed(children):
   try:os.killpg(p.pid,signal.SIGTERM)
   except ProcessLookupError:pass
  for p in reversed(children):
   try:p.wait(timeout=5)
   except subprocess.TimeoutExpired:p.kill();p.wait()
  # Renderer shutdown can finish writing Mesa's cache after its parent exits.
  for attempt in range(30):
   try:shutil.rmtree(config);break
   except FileNotFoundError:break
   except OSError as error:
    if error.errno!=errno.ENOTEMPTY or attempt==29:raise
    time.sleep(.1)
  output.close()
