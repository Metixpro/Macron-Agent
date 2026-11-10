#!/usr/bin/env python3
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SOURCE=Path(__file__).resolve().parents[1]

class InstallationLifecycleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build=tempfile.TemporaryDirectory(prefix="macron-lifecycle-build-")
        cls.binary=Path(cls.build.name)/"macron"
        supplied=os.environ.get("MACRON_TEST_BINARY")
        if supplied:
            cls.binary=Path(supplied).resolve()
        else:
            subprocess.run(["go","build","-buildvcs=false","-o",str(cls.binary),"./cmd/macron"],cwd=SOURCE,check=True,timeout=180)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory(prefix="macron-lifecycle-")
        self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.user=self.root/"user";self.user.mkdir()
        self.prefix=self.root/"private bin";self.prefix.mkdir()
        self.state=self.root/"private state"
        self.target=self.prefix/"macron"
        self.env={**os.environ,"HOME":str(self.user),"SHELL":"/bin/bash","NO_COLOR":"1"}
        for key in ["MACRON_HOME","MACRON_PROFILE","MACRON_ENV_FILE"]:self.env.pop(key,None)
        args=["bash",str(SOURCE/"install.sh"),"--binary",str(self.binary),"--scope","user","--prefix",str(self.prefix),"--home",str(self.state),"--no-tools","--no-service","--no-setup","--yes"]
        result=subprocess.run(args,env=self.env,capture_output=True,text=True,timeout=30)
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        (self.state/"workspaces"/"example").mkdir(parents=True)
        (self.state/"workspaces"/"example"/"owned-file").write_text("owned by installation")
        (self.state/"private-key").write_text("private fixture")
        self.other=self.root/"unrelated";self.other.write_text("keep me")

    def run_cli(self,*args):
        return subprocess.run([str(self.target),*args],env=self.env,capture_output=True,text=True,timeout=30)

    def test_receipt_discovers_custom_home_and_full_uninstall(self):
        path=self.run_cli("config","path")
        self.assertEqual(path.returncode,0,path.stderr)
        self.assertIn(str(self.state/"config.env"),path.stdout)
        preview=self.run_cli("uninstall","--dry-run")
        self.assertEqual(preview.returncode,0,preview.stdout+preview.stderr)
        self.assertIn(str(self.state),preview.stdout)
        self.assertTrue(self.target.exists());self.assertTrue(self.state.exists())
        removed=self.run_cli("uninstall","--yes")
        self.assertEqual(removed.returncode,0,removed.stdout+removed.stderr)
        self.assertFalse(self.target.exists());self.assertFalse(self.state.exists())
        self.assertFalse((self.prefix/".macron-install").exists())
        self.assertEqual(self.other.read_text(),"keep me")
        self.assertNotIn("macron PATH",(self.user/".bashrc").read_text())

    def test_noninteractive_uninstall_requires_explicit_confirmation(self):
        result=self.run_cli("uninstall")
        self.assertEqual(result.returncode,2,result.stdout+result.stderr)
        self.assertTrue(self.target.exists());self.assertTrue(self.state.exists())

    def test_keep_data_removes_program_and_retains_private_state(self):
        result=self.run_cli("uninstall","--yes","--keep-data")
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        self.assertFalse(self.target.exists())
        self.assertEqual((self.state/"private-key").read_text(),"private fixture")
        self.assertEqual(self.other.read_text(),"keep me")

if __name__=="__main__":unittest.main()
