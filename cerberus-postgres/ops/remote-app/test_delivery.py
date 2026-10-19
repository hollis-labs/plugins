import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import deliver
import receive

class DeliveryTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name);self.target = self.root / 'remote';self.target.mkdir(mode=0o700)
        self.audit = self.root / 'audit';self.audit.mkdir(mode=0o700)
        self.source = self.root / 'secret';self.source.write_text('x'*43);self.source.chmod(0o600)
        self.cfg = dict(app='example',host='postgres.example.test',port=15438,database='example',user='cb_example_r2',reference='file:///broker/issued/test/password',directory=str(self.target),ca_file='')
        self.payload = {k:self.cfg[k] for k in ('app','host','port','database','user','reference')};self.payload.update(password='x'*43,delivery_id='a'*32)
    def test_receiver_refuses_wrong_identity_before_writing(self):
        for key in ('app','user','database','reference','host'):
            payload = dict(self.payload);payload[key]='wrong'
            with self.assertRaises(ValueError):receive.receive(self.cfg,payload,lambda *_:None)
        self.assertFalse(list(self.target.iterdir()))
    def test_private_generation_and_replay_refusal(self):
        result=receive.receive(self.cfg,self.payload,lambda *_:None)
        dest=Path(result['credential_file']);self.assertEqual(dest.stat().st_mode&0o777,0o600);self.assertFalse(result['activated'])
        self.assertEqual(json.loads(dest.read_text())['password'],'x'*43)
        with self.assertRaises(FileExistsError):receive.receive(self.cfg,self.payload,lambda *_:None)
    def test_failed_probe_never_installs_password(self):
        def fail(*_):raise RuntimeError('TLS failed')
        with self.assertRaises(RuntimeError):receive.receive(self.cfg,self.payload,fail)
        self.assertEqual((self.target/('a'*32+'.json')).read_bytes(),b'')
    def test_symlink_source_refused(self):
        link=self.root/'link';link.symlink_to(self.source)
        with self.assertRaises(OSError):receive.private_read(link)
    def test_sender_bound_plan_and_private_transport(self):
        cfg=dict(self.cfg,ssh_host='station.example.test',ssh_user='app',ssh_port=22,identity_file='/private/key',known_hosts='/private/known_hosts',receiver='/opt/receive.py',receiver_profile='/private/profile',audit_directory=str(self.audit))
        preview={'digest':'reviewed','reference':self.cfg['reference']}
        called=[]
        def transport(argv,**kwargs):
            self.assertNotIn('x'*43,' '.join(argv));self.assertNotIn('env',kwargs);self.assertIn('StrictHostKeyChecking=yes',argv)
            payload=json.loads(kwargs['input']);called.append(payload)
            result=receive.receive(self.cfg,payload,lambda *_:None)
            return subprocess.CompletedProcess(argv,0,json.dumps(result).encode(),b'')
        with self.assertRaises(ValueError):deliver.apply(cfg,self.source,preview,'wrong',True,transport)
        self.assertEqual(called,[])
        result=deliver.apply(cfg,self.source,preview,'reviewed',True,transport);self.assertTrue(result['ok'])
        for path in self.audit.iterdir():self.assertNotIn('x'*43,path.read_text())

if __name__=='__main__':unittest.main()
