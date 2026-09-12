"""Consume actual Client.raw HTTP status handling; only transport is in memory."""
import http.cookiejar
import io
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import smoke


class SessionRevocationTests(unittest.TestCase):
    def client(self, logout, replay):
        client=smoke.Client.__new__(smoke.Client)
        client.base='https://console.example.test';client.records=[];client.csrf='x'*43
        client.cookies=http.cookiejar.CookieJar()
        client.cookies.set_cookie(http.cookiejar.Cookie(0,'xm_session','test-session',None,False,'console.example.test',False,False,'/',True,True,None,True,None,None,{}))
        seen=[]
        class Response(io.BytesIO):
            def __init__(self,status,payload):
                super().__init__(json.dumps(payload).encode());self.code=status;self.headers={'Content-Type':'application/json'}
        class Opener:
            def open(self,request,timeout):
                method=request.get_method();seen.append((method,[c.value for c in client.cookies]))
                if method=='POST':client.cookies.clear()
                return Response(*(logout if method=='POST' else replay))
        client.opener=Opener()
        return client,seen

    def test_success_requires_explicit_logout_and_exact_false_after_original_cookie_replay(self):
        for kind in ('user','staff'):
            client,seen=self.client((200,{'ok':True}),(200,{'authenticated':False}))
            client.revoke(kind)
            self.assertEqual(seen,[('POST',['test-session']),('GET',['test-session'])])
            self.assertEqual(list(client.cookies),[])

    def test_error_responses_or_missing_false_never_prove_revocation(self):
        cases=[((403,{'error':'temporary_denial'}),(403,{'error':'temporary_denial'})),
               ((200,{}),(200,{'authenticated':False})),
               ((200,{'ok':True}),(403,{'error':'denied'})),
               ((200,{'ok':True}),(401,{'error':{'code':'AUTH_REQUIRED'}})),
               ((200,{'ok':True}),(200,{})),
               ((200,{'ok':True}),(200,{'authenticated':0})),
               ((200,{'ok':True}),(200,{'authenticated':'false'})),
               ((200,{'ok':True}),(200,{'authenticated':False,'error':'temporary_denial'}))]
        for kind in ('user','staff'):
            for logout,replay in cases:
                client,_=self.client(logout,replay)
                with self.subTest(kind=kind,logout=logout,replay=replay),self.assertRaises(smoke.SmokeFailure):client.revoke(kind)
                self.assertEqual(list(client.cookies),[])

    def test_only_explicit_user_auth_required_may_already_be_expired(self):
        client,_=self.client((401,{'error':{'code':'AUTH_REQUIRED','message':'authentication is required'}}),(200,{'authenticated':False}))
        client.revoke('user')
        for kind,error in [('user',{'code':'TEMPORARY_DENIAL'}),('staff',{'code':'AUTH_REQUIRED'})]:
            client,_=self.client((401,{'error':error}),(200,{'authenticated':False}))
            with self.subTest(kind=kind,error=error),self.assertRaises(smoke.SmokeFailure):client.revoke(kind)


if __name__=='__main__':unittest.main()
