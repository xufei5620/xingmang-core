"""Synthetic forms of the two native-valid server parser failures, without values."""
import hashlib
from pathlib import Path
import tempfile
import types
import unittest
from test_host_nginx import nginx, vhost


LUA = '''
        -- a line comment with } { ; include /not-nginx;
        local one = "escaped quote \\\" and } ; proxy_pass"
        local two = 'escaped quote \\\' and {'
        local long = [==[ } { ; " include /not-nginx; ]=] ]==]
        --[=[ } { ; a long comment ]==] ]=]
        local nested = { outer = { inner = "}" }, value = '{' }
        return nested
    '''


class NativeNginxSyntaxTests(unittest.TestCase):
    def test_json_log_format_quoted_braces_are_arguments_not_blocks(self):
        m=nginx(self)
        self.assertEqual(m.parse('log_format public escape=json \'{\'\n\'"status":"$status"\'\n\'}\';'),
                         [(('log_format','public','escape=json','{','"status":"$status"','}'),None)])
        self.assertEqual(m.parse('set $marker ";"; set $empty "";'),
                         [(('set','$marker',';'),None),(('set','$empty',''),None)])

    def test_observed_if_set_by_lua_block_retains_complete_opaque_bytes(self):
        m=nginx(self);value='if ($request_uri) { set_by_lua_block $check {'+LUA+'} }'
        nodes=m.parse(value);opaque=nodes[0][1][0][1]
        self.assertEqual(opaque.sha256,hashlib.sha256(LUA.encode()).hexdigest())
        self.assertEqual(opaque.byte_length,len(LUA.encode()))
        self.assertNotEqual(opaque,m.parse('include /not-nginx;'))
        self.assertEqual(nodes[0][0],('if','($request_uri)'))
        self.assertEqual(nodes[0][1][0][0],('set_by_lua_block','$check'))

    def test_nginx_unknown_escapes_and_literal_structural_arguments_preserve_semantics(self):
        m=nginx(self)
        left=r'proxy_set_header X-Test "\q";';right='proxy_set_header X-Test "q";'
        self.assertEqual(m.parse(left),[(('proxy_set_header','X-Test',r'\q'),None)])
        with self.assertRaises(m.OperatorError):m.only_proxy_changes(left,right)
        self.assertEqual(m.parse(r'set $x "\t\r\n\\\"\'";')[0][0][-1],'\t\r\n\\"\'')
        self.assertEqual(m.parse('set $x ${server_name};'),[(('set','$x','${server_name}'),None)])

    def test_comment_and_long_bracket_boundaries_are_exact(self):
        m=nginx(self)
        for body in ('-- [=[ }\nreturn 1\n', '--[=[ } ]=]\nreturn 1\n',
                     'local text=[==[ } ]=] \\" include x; ]==]\nreturn text\n',
                     r'''local text='\\'; local other="\\\" }"; return {nested={1}}'''):
            with self.subTest(body=body):
                opaque=m.parse('set_by_lua_block $x {'+body+'}')[0][1]
                self.assertEqual(opaque.sha256,hashlib.sha256(body.encode()).hexdigest())

    def test_lua_comments_whitespace_and_values_are_not_normalized_away(self):
        m=nginx(self);value='set_by_lua_block $check {'+LUA+'}'
        m.only_proxy_changes(value,value)
        for changed in (value.replace('a line comment','another comment'),value.replace('return nested','return 0'),
                        value.replace('        local one',' local one'),value.replace('set_by_lua_block $check','set_by_lua_block $other')):
            with self.subTest(case=changed[-20:]),self.assertRaises(m.OperatorError):m.only_proxy_changes(value,changed)

    def test_staged_main_never_rewrites_include_words_inside_lua(self):
        m=nginx(self)
        value='events {} http { set_by_lua_block $v { local text = "include /live.conf;"; return text } include /live.conf; }'
        result=m.staged_main(value.encode(),[('/live.conf','/candidate.conf')]).decode()
        self.assertIn('local text = "include /live.conf;"',result)
        self.assertTrue(result.endswith('include "/candidate.conf"; }'))
        self.assertEqual(m.parse(value)[1][1][0],m.parse(result)[1][1][0])

    def test_lua_is_not_an_authorized_managed_route_override(self):
        m=nginx(self)
        for insertion in ('set_by_lua_block $x { return "safe-looking" }',
                          'if ($request_uri) { set_by_lua_block $x { return "safe-looking" } }',
                          'content_by_lua "ngx.exit(403)";', 'rewrite_by_lua_file /not-reviewed.lua;'):
            candidate=vhost().replace('location / {','location / { '+insertion)
            with self.subTest(insertion=insertion),self.assertRaises(m.OperatorError):
                m.check_routes(candidate,{'admin':'https://console.example.com'},{'admin':8088})

    def test_unclosed_lua_strings_comments_tables_and_unknown_handlers_reject(self):
        m=nginx(self)
        for body in ('local x = "missing',"local x = 'missing",'local x = [=[missing',
                     '--[==[missing','local x = { nested = { 1 }','-- no final delimiter'):
            with self.subTest(body=body),self.assertRaises(m.OperatorError):m.parse('set_by_lua_block $x {'+body)
        with self.assertRaises(m.OperatorError):m.parse('unreviewed_by_lua_block { return 1 }')

    def test_loaded_graph_does_not_follow_include_words_inside_frozen_lua(self):
        m=nginx(self)
        from nginx_layout import FrozenLayout,verify_layout
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);main=root/'main.conf';live=root/'live.conf';shared=root/'shared.conf';stage=root/'stage.tmp';candidate=root/'candidate.conf'
            main.write_text('events {} http { include '+repr(str(live))+'; include '+repr(str(shared))+'; }')
            live.write_text(vhost());shared.write_text('log_format sample "{" "}"; if ($uri) { set_by_lua_block $v {'+LUA+'} }')
            loaded={str(path):path.read_bytes().decode('utf-8').strip() for path in (main,live,shared)}
            host=types.SimpleNamespace(driver=types.SimpleNamespace(config={'mode':'server-rehearsal'}),main=main,staged=stage,rows=[(live,candidate)],runtime_path=str)
            proof=FrozenLayout(host,loaded).proof()
            self.assertEqual(set(proof['files']),set(loaded));verify_layout(host,proof,{})
            original=shared.read_bytes();shared.write_bytes(original.replace(b'local one',b'local ONE'))
            with self.assertRaises(m.OperatorError):verify_layout(host,proof,{})


if __name__=='__main__':unittest.main()
