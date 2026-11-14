param([string]$Binary="$env:USERPROFILE\spectrune-new.exe",[string]$LogFile="$env:USERPROFILE\spectrune-single-repeat.log")
$ErrorActionPreference='Stop'
$log=$LogFile
'Windows single-window audit started' | Set-Content $log
Add-Type -TypeDefinition @'
using System;using System.Text;using System.Collections.Generic;using System.Diagnostics;using System.Runtime.InteropServices;
public class SingleWindow {
 public class Info {public IntPtr Hwnd;public uint Pid;}
 public delegate bool Callback(IntPtr h,IntPtr p);
 [DllImport("user32.dll")]static extern bool EnumWindows(Callback c,IntPtr p);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)]static extern int GetWindowText(IntPtr h,StringBuilder b,int n);
 [DllImport("user32.dll")]static extern uint GetWindowThreadProcessId(IntPtr h,out uint p);
 [DllImport("user32.dll")]public static extern bool ShowWindow(IntPtr h,int c);
 [DllImport("user32.dll")]public static extern bool IsWindowVisible(IntPtr h);
 [DllImport("user32.dll")]public static extern bool IsIconic(IntPtr h);
 [DllImport("user32.dll")]public static extern IntPtr GetForegroundWindow();
 public static Info[] Find(){var r=new List<Info>();EnumWindows((h,p)=>{var b=new StringBuilder(128);GetWindowText(h,b,128);if(b.ToString()=="Spectrune"){uint id;GetWindowThreadProcessId(h,out id);try{if(Process.GetProcessById((int)id).ProcessName.StartsWith("spectrune",StringComparison.OrdinalIgnoreCase))r.Add(new Info{Hwnd=h,Pid=id});}catch{}}return true;},IntPtr.Zero);return r.ToArray();}
}
'@
$binary=$Binary
$started=[System.Collections.Generic.List[System.Diagnostics.Process]]::new()
$owned=$false
try {
 $initial=@([SingleWindow]::Find())
 if($initial.Count -gt 1){throw "Already have $($initial.Count) windows before test"}
 if($initial.Count -eq 0){$first=Start-Process $binary -ArgumentList '/gui' -PassThru;$started.Add($first);$owned=$true}
 for($i=0;$i -lt 60;$i++){ $windows=@([SingleWindow]::Find());if($windows.Count -eq 1 -and [SingleWindow]::IsWindowVisible($windows[0].Hwnd)){break};Start-Sleep -Milliseconds 250 }
 if($windows.Count -ne 1){throw 'Exactly one primary window expected'}
 $original=$windows[0];$handle=$original.Hwnd;$guiProcessID=$original.Pid
 function Assert-SameWindow {
  $all=@([SingleWindow]::Find())
  if($all.Count -ne 1 -or $all[0].Hwnd -ne $handle -or $all[0].Pid -ne $guiProcessID){throw 'Window identity or instance count changed'}
 }
 function Launch-Again($argument='/gui') {
  $p=Start-Process $binary -ArgumentList $argument -PassThru;$started.Add($p)
  if(!$p.WaitForExit(15000)){throw 'Secondary instance stayed alive'}
  if($p.ExitCode -ne 0){throw 'Secondary exited with error'}
  Assert-SameWindow
 }
 Launch-Again
 "PASS repeated launch keeps PID=$guiProcessID HWND=$($handle.ToInt64())" | Add-Content $log
 $burst=@();for($i=0;$i -lt 6;$i++){$p=Start-Process $binary -ArgumentList '/gui' -PassThru;$started.Add($p);$burst+= $p}
 foreach($p in $burst){if(!$p.WaitForExit(15000)){throw 'Burst secondary stayed alive'}}
 Assert-SameWindow
 'PASS six repeated launches keep one window' | Add-Content $log
 Launch-Again '/gui-restart'
 'PASS /gui-restart reuses an existing live window' | Add-Content $log
 [SingleWindow]::ShowWindow($handle,6) | Out-Null
 if(![SingleWindow]::IsIconic($handle)){throw 'Failed to minimize'}
 Launch-Again
 if([SingleWindow]::IsIconic($handle) -or ![SingleWindow]::IsWindowVisible($handle)){throw 'Minimized original was not restored'}
 if([SingleWindow]::GetForegroundWindow() -ne $handle){throw 'Original was not foreground after repeat'}
 'PASS minimized original restored and focused' | Add-Content $log
 [SingleWindow]::ShowWindow($handle,0) | Out-Null
 Launch-Again
 if(![SingleWindow]::IsWindowVisible($handle)){throw 'Tray-hidden original was not restored'}
 if([SingleWindow]::GetForegroundWindow() -ne $handle){throw 'Restored original not foreground'}
 'PASS tray-hidden original restored and focused with same PID/HWND' | Add-Content $log
 if($owned){
  $first.Kill();$first.WaitForExit()
  $raced=@();for($i=0;$i -lt 6;$i++){$p=Start-Process $binary -ArgumentList '/gui' -PassThru;$started.Add($p);$raced+=$p}
  Start-Sleep -Seconds 6
  $alive=@($raced | Where-Object {!$_.HasExited})
  if($alive.Count -ne 1 -or @([SingleWindow]::Find()).Count -ne 1){throw 'First-launch race created duplicates'}
  'PASS simultaneous first launches converge to one GUI' | Add-Content $log
 }
 'PASS Windows single-window audit' | Add-Content $log
} catch {$_ | Out-String | Add-Content $log;exit 1}
finally {
 if($owned){foreach($p in $started){try{if(!$p.HasExited){$p.Kill();$p.WaitForExit()}}catch{}}}
}
