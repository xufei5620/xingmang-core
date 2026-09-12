"""N-2: distinguish response headers from body/EOF failures without retries."""
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import smoke


class TransportPhaseTests(unittest.TestCase):
    def test_received_200_body_timeout_is_not_reported_as_missing_http_status(self):
        calls=[]
        class Response:
            code=200
            headers={'Content-Type':'application/json'}
            def __enter__(self): return self
            def __exit__(self,*args): return False
            def read(self,*args): raise TimeoutError('fixture body has not terminated')
        client=smoke.Client.__new__(smoke.Client)
        client.base='https://localhost:18443';client.csrf='';client.records=[]
        client.opener=SimpleNamespace(open=lambda request,**kw:calls.append(kw) or Response())
        with self.assertRaisesRegex(smoke.SmokeFailure,'HTTP_TRANSPORT_FAILED'):
            client.raw('GET','/invoice-api/v1/admin/invoice-requests')
        self.assertEqual(calls,[{'timeout':30}],'no timeout extension or retry')
        row=client.records[0]
        self.assertEqual(row['status'],200)
        self.assertEqual(row.get('transport_phase'),'body')
        self.assertIsNotNone(row.get('headers_received_utc'))
        self.assertIsNone(row.get('response_bytes'),'do not invent a partial-byte count')

    def test_header_timeout_retains_unknown_status_and_one_attempt(self):
        calls=[]
        def fail(*args,**kw):calls.append(kw);raise TimeoutError('fixture headers unavailable')
        client=smoke.Client.__new__(smoke.Client)
        client.base='https://localhost:18443';client.csrf='';client.records=[];client.opener=SimpleNamespace(open=fail)
        with self.assertRaises(smoke.SmokeFailure):client.raw('GET','/readyz')
        self.assertEqual(len(calls),1)
        self.assertIsNone(client.records[0]['status'])
        self.assertEqual(client.records[0].get('transport_phase'),'connect_or_headers')


if __name__=='__main__':unittest.main()
