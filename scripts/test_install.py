#!/usr/bin/env python3
import json
import os
from pathlib import Path
import pty
import select
import subprocess
import sys
import tempfile
import time
import unittest

INSTALLER=Path(__file__).resolve().parents[1]/"install.sh"

class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory(prefix="macron-installer-")
        self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.prefix=self.root/"bin with spaces";self.prefix.mkdir()
        self.home=self.root/"user-home";self.home.mkdir()
        self.state=self.root/"state"
        self.target=self.prefix/"macron"
        self.target.write_text("#!/bin/sh\necho 'macron old'\n");self.target.chmod(0o755)
        self.commands=self.root/"commands.jsonl"
        self.env={**os.environ,"HOME":str(self.home),"MACRON_HOME":str(self.state),"MACRON_TEST_COMMAND_LOG":str(self.commands),"SHELL":"/bin/bash","NO_COLOR":"1"}

    def candidate(self,body="echo 'macron 9.9.9'"):
        p=self.root/"candidate";p.write_text("#!/bin/sh\n"+body+"\n");p.chmod(0o755);return p

    def args(self,body="echo 'macron 9.9.9'",extra=(),tools=False):
        args=["bash",str(INSTALLER),"--binary",str(self.candidate(body)),"--prefix",str(self.prefix),"--no-setup","--no-service",*extra]
        if not tools:args.append("--no-tools")
        return args

    def install(self,body="echo 'macron 9.9.9'",*extra,env=None,tools=False):
        return subprocess.run(self.args(body,extra,tools),env={**self.env,**(env or {})},capture_output=True,text=True,timeout=30,stdin=subprocess.DEVNULL)

    def fake_python(self,old=False,broken_venv=False,broken_pip=False):
        stubs=self.root/"stubs";stubs.mkdir(exist_ok=True)
        script=f'''#!{sys.executable}
import json,os,pathlib,sys
args=sys.argv[1:]
with open(os.environ["MACRON_TEST_COMMAND_LOG"],"a") as f:f.write(json.dumps([sys.argv[0],*args])+"\\n")
if args[:1]==["-c"]:sys.exit(0)
if args[:2]==["-m","venv"]:
 if {broken_venv!r}:sys.exit(1)
 target=pathlib.Path(args[-1]);(target/"bin").mkdir(parents=True,exist_ok=True)
 (target/"bin"/"python").write_text(pathlib.Path(__file__).read_text());(target/"bin"/"python").chmod(0o755)
 sys.exit(0)
if args[:3]==["-m","pip","install"]:sys.exit({int(broken_pip)})
sys.exit(0)
'''
        good=stubs/"managed-python";good.write_text(script);good.chmod(0o755)
        for name in ["python3","python3.12","python3.13"]:
            p=stubs/name;p.write_text("#!/bin/sh\nexit 1\n" if old else script);p.chmod(0o755)
        uv=stubs/"uv"
        uv.write_text(f'''#!{sys.executable}
import json,os,pathlib,sys
args=sys.argv[1:]
with open(os.environ["MACRON_TEST_COMMAND_LOG"],"a") as f:f.write(json.dumps(["uv",*args])+"\\n")
if args[:2]==["python","find"]:print({str(good)!r})
if args[:1]==["venv"]:
 target=pathlib.Path(args[-1]);(target/"bin").mkdir(parents=True,exist_ok=True)
 (target/"bin"/"python").write_text(pathlib.Path({str(good)!r}).read_text());(target/"bin"/"python").chmod(0o755)
''');uv.chmod(0o755)
        for name in ["sudo","apt-get","dnf","brew"]:
            p=stubs/name;p.write_text("#!/bin/sh\necho FORBIDDEN_SYSTEM_PACKAGE_CALL >&2\nexit 99\n");p.chmod(0o755)
        return {"PATH":f"{stubs}:{os.environ['PATH']}"}

    def calls(self):
        return [json.loads(l) for l in self.commands.read_text().splitlines()]

    def test_help_when_piped(self):
        r=subprocess.run(["bash","-s","--","--help"],input=INSTALLER.read_text(),capture_output=True,text=True,timeout=10)
        self.assertEqual(r.returncode,0,r.stderr)
        for flag in ["--scope","--home","--no-tools","--no-service","--yes","--uninstall"]:self.assertIn(flag,r.stdout)

    def test_version_paths_and_receipt(self):
        r=self.install("echo 'macron 9.9.9'","--yes","--scope","user")
        self.assertEqual(r.returncode,0,r.stderr)
        self.assertIn("updated old → 9.9.9",r.stdout)
        self.assertIn("macron setup",r.stdout)
        receipt=(self.prefix/".macron-install").read_text()
        self.assertIn("scope=user\n",receipt);self.assertIn(f"home={self.state}\n",receipt)
        self.assertNotIn("\\n",receipt)
        self.assertIn("[1/6]",r.stdout);self.assertIn("[6/6]",r.stdout)

    def test_user_scope_installs_python_without_sudo(self):
        r=self.install(env=self.fake_python(),tools=True)
        self.assertEqual(r.returncode,0,r.stderr)
        self.assertTrue((self.state/"venv/bin/python").exists())
        self.assertTrue(any(c[1:4]==["-m","pip","install"] for c in self.calls()))
        self.assertNotIn("FORBIDDEN_SYSTEM_PACKAGE_CALL",r.stdout+r.stderr)

    def test_old_python_uses_private_managed_runtime(self):
        r=self.install(env=self.fake_python(old=True),tools=True)
        self.assertEqual(r.returncode,0,r.stdout+r.stderr)
        self.assertIn(["uv","python","install","3.12"],self.calls())
        self.assertTrue((self.state/"venv/bin/python").exists())
        self.assertNotIn("FORBIDDEN_SYSTEM_PACKAGE_CALL",r.stdout+r.stderr)

    def test_missing_venv_uses_uv_without_system_packages(self):
        r=self.install(env=self.fake_python(broken_venv=True),tools=True)
        self.assertEqual(r.returncode,0,r.stdout+r.stderr)
        self.assertTrue(any(c[:2]==["uv","venv"] for c in self.calls()))
        self.assertTrue((self.state/"venv/bin/python").exists())

    def test_python_failure_is_not_reported_as_success(self):
        r=self.install(env=self.fake_python(broken_pip=True),tools=True)
        self.assertNotEqual(r.returncode,0)
        self.assertIn("Python tools did not install",r.stderr)
        retry=self.install(env={"PATH":str(self.root/"stubs")+":"+os.environ["PATH"]})
        self.assertEqual(retry.returncode,0,retry.stderr)

    def test_broken_candidate_keeps_old_binary(self):
        r=self.install("exit 42")
        self.assertNotEqual(r.returncode,0)
        self.assertIn("macron old",self.target.read_text())
        self.assertEqual(list(self.prefix.glob(".macron-install.*")),[])

    def test_backup_and_unrelated_symlink(self):
        victim=self.root/"unrelated";victim.write_text("keep")
        (self.prefix/"macron.new").symlink_to(victim)
        r=self.install()
        self.assertEqual(r.returncode,0,r.stderr)
        self.assertEqual(victim.read_text(),"keep")
        self.assertIn("macron old",(self.prefix/"macron.old").read_text())
        self.assertEqual(list(self.prefix.glob(".macron-install.*")),[])

    def test_rejects_unsafe_data_path(self):
        r=self.install("echo 'macron 9.9.9'","--home",str(self.home))
        self.assertNotEqual(r.returncode,0)
        self.assertIn("macron old",self.target.read_text())

    def test_unsafe_paths_cannot_be_disguised(self):
        link=self.root/"parent-link";link.symlink_to(self.home,target_is_directory=True)
        for path in [str(self.home)+"/", str(self.home)+"/../user-home", str(link)+"/."]:
            with self.subTest(path=path):
                r=self.install("echo 'macron 9.9.9'","--home",path)
                self.assertNotEqual(r.returncode,0,r.stdout+r.stderr)
                self.assertFalse((self.home/".macron-home").exists())
                self.assertIn("macron old",self.target.read_text())

    def test_rejects_symlink_data_home(self):
        actual=self.root/"actual";actual.mkdir();self.state.symlink_to(actual,target_is_directory=True)
        r=self.install()
        self.assertNotEqual(r.returncode,0)
        self.assertIn("symlink",r.stderr)
        self.assertEqual(list(actual.iterdir()),[])

    def test_unknown_scope_does_not_mutate_install(self):
        r=self.install("echo 'macron 9.9.9'","--scope","everyone")
        self.assertNotEqual(r.returncode,0)
        self.assertIn("macron old",self.target.read_text())

    def test_interactive_scope_is_asked_before_installation(self):
        args=self.args()
        pid,fd=pty.fork()
        if pid==0:
            os.execvpe(args[0],args,self.env)
        output=b"";selected=False;confirmed=False;deadline=time.monotonic()+15
        try:
            while time.monotonic()<deadline:
                ready,_,_=select.select([fd],[],[],0.1)
                if ready:
                    try:chunk=os.read(fd,65536)
                    except OSError:break
                    if not chunk:break
                    output+=chunk
                    if b"Installation [" in output and not selected:
                        self.assertIn("macron old",self.target.read_text())
                        os.write(fd,b"1\n");selected=True
                    if b"Continue?" in output and not confirmed:os.write(fd,b"Y\n");confirmed=True
                done,status=os.waitpid(pid,os.WNOHANG)
                if done:break
            else:
                os.kill(pid,15);self.fail(output.decode(errors="replace"))
        finally:
            os.close(fd)
            try:os.waitpid(pid,0)
            except ChildProcessError:pass
        self.assertTrue(selected and confirmed,output.decode(errors="replace"))
        self.assertIn(b"Scope    user",output)
        self.assertIn("macron 9.9.9",self.target.read_text())

    def test_system_scope_reexecutes_with_the_reviewed_paths(self):
        stubs=self.root/"root-stubs";stubs.mkdir()
        (stubs/"id").write_text("#!/bin/sh\ncase \"$1\" in -u) [ \"${MACRON_TEST_ROOT:-0}\" = 1 ] && echo 0 || echo 1000;; -un) echo fixture;; *) /usr/bin/id \"$@\";; esac\n")
        (stubs/"sudo").write_text("#!/bin/sh\nwhile [ \"$1\" = -H ] || [ \"$1\" = -n ]; do shift; done\nMACRON_TEST_ROOT=1 exec \"$@\"\n")
        for f in stubs.iterdir():f.chmod(0o755)
        r=self.install("echo 'macron 9.9.9'","--scope","system","--yes",env={"PATH":str(stubs)+":"+os.environ["PATH"]})
        self.assertEqual(r.returncode,0,r.stdout+r.stderr)
        self.assertIn("scope=system",(self.prefix/".macron-install").read_text())
        self.assertIn(f"home={self.state}",(self.prefix/".macron-install").read_text())
        self.assertIn("uid=0",(self.prefix/".macron-install").read_text())

    def test_shell_path_with_quotes_is_literal_and_valid(self):
        self.prefix=self.root/"bin 'quoted' $(touch SHOULD_NOT_EXIST)";self.prefix.mkdir()
        self.target=self.prefix/"macron"
        r=self.install()
        self.assertEqual(r.returncode,0,r.stdout+r.stderr)
        rc=self.home/".bashrc"
        checked=subprocess.run(["bash","-n",str(rc)],capture_output=True,text=True)
        self.assertEqual(checked.returncode,0,checked.stderr)
        sourced=subprocess.run(["bash","--noprofile","--norc","-c",'source "$1"; printf "%s" "$PATH"',"fixture",str(rc)],env=self.env,capture_output=True,text=True,cwd=self.home)
        self.assertEqual(sourced.returncode,0,sourced.stderr)
        self.assertIn(str(self.prefix),sourced.stdout)
        self.assertFalse((self.home/"SHOULD_NOT_EXIST").exists())

if __name__=="__main__":unittest.main()
