import datetime
import json
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch
import backup

class BackupTest(unittest.TestCase):
    def test_rejects_changed_image_and_volume(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'inventory';cfg={'container':'fixture','image_id':'sha256:'+'a'*64,'volume':'fixture-data','stanza':'shared','max_backup_age_hours':26};path.write_text(json.dumps(cfg));path.chmod(0o600)
            info={'Image':cfg['image_id'],'State':{'Running':True},'Mounts':[{'Destination':'/var/lib/postgresql/data','Name':cfg['volume']}]}
            with patch.object(backup,'invoke',lambda *_:json.dumps([info]).encode()):
                self.assertEqual(backup.load(path)['stanza'],'shared')
                info['Image']='sha256:'+'b'*64
                with self.assertRaises(ValueError):backup.load(path)
                info['Image']=cfg['image_id'];info['Mounts'][0]['Name']='another-volume'
                with self.assertRaises(ValueError):backup.load(path)
    def test_fresh_encrypted_chain_and_archive_health(self):
        cfg={'container':'fixture','stanza':'shared','max_backup_age_hours':26}
        stamp=datetime.datetime.now(datetime.timezone.utc).timestamp()
        metadata=[{'name':'shared','cipher':'aes-256-cbc','status':{'code':0},'backup':[{'timestamp':{'stop':stamp},'label':'fixture'}]}]
        health={'archive_mode':'on','pending_wal':0,'oldest_pending_seconds':0}
        def run(argv,**kwargs):
            if argv[-1]=='info':return json.dumps(metadata).encode()
            if 'psql' in argv:return json.dumps(health).encode()
            return b'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/test 100 20 80 20% /data\n'
        with patch.object(backup,'invoke',run):
            self.assertEqual(backup.run(cfg,'status')['repository_status'],'ok')
            metadata[0]['cipher']='none'
            with self.assertRaises(RuntimeError):backup.run(cfg,'status')
            metadata[0]['cipher']='aes-256-cbc';health['oldest_pending_seconds']=301
            with self.assertRaises(RuntimeError):backup.run(cfg,'status')
            health['oldest_pending_seconds']=0;metadata[0]['backup'][0]['timestamp']['stop']=stamp-27*3600
            with self.assertRaises(RuntimeError):backup.run(cfg,'status')
    def test_overlapping_runs_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            with backup.lock(tmp+'/inventory'):
                with self.assertRaises(BlockingIOError):backup.lock(tmp+'/inventory')

if __name__=='__main__':unittest.main()
