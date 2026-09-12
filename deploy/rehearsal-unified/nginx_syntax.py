"""Nginx token boundaries and byte-bound opaque bodies; never execute Lua."""
from dataclasses import dataclass
import hashlib
import re

from lifecycle import require

LONG_OPEN=re.compile(r'\[(=*)\[')


@dataclass(frozen=True)
class OpaqueLua:
    sha256: str
    byte_length: int


class Reader:
    def __init__(self,value):
        require(len(value)<=8*1024*1024,'public nginx syntax exceeds size bound')
        self.value=value;self.position=0

    def escaped(self):
        """Match ngx_conf_read_token: retain backslashes on unknown escapes."""
        self.position+=1
        require(self.position<len(self.value),'incomplete public nginx escape')
        char=self.value[self.position];self.position+=1
        return {'t':'\t','r':'\r','n':'\n','\\':'\\',"'":"'",'"':'"'}.get(char,'\\'+char)

    def next(self):
        text=self.value;size=len(text)
        while self.position<size:
            char=text[self.position]
            if char in ' \t\r\n':self.position+=1;continue
            if char=='#':
                end=text.find('\n',self.position);self.position=size if end<0 else end+1;continue
            break
        if self.position==size:return None
        start=self.position;char=text[start]
        if char in '{};':self.position+=1;return ('symbol',char,start,self.position)
        word=[]
        if char in ('"',"'"):
            quote=char;self.position+=1
            while self.position<size and text[self.position]!=quote:
                if text[self.position]=='\\':word.append(self.escaped())
                else:word.append(text[self.position]);self.position+=1
            require(self.position<size,'unterminated public nginx quote')
            self.position+=1
            require(self.position==size or text[self.position] in ' \t\r\n;{)',
                    'quoted nginx argument has no separator')
        else:
            while self.position<size:
                char=text[self.position]
                if char in ' \t\r\n;':break
                if char=='{' and not (self.position>start and text[self.position-1]=='$'):break
                if char=='\\':word.append(self.escaped())
                else:word.append(char);self.position+=1
        require(self.position>start,'invalid public nginx token')
        return ('word',''.join(word),start,self.position)

    def lua_body(self):
        """Find the closing brace in Lua code, excluding strings/comments."""
        text=self.value;start=self.position;depth=1
        def long_string(at):
            match=LONG_OPEN.match(text,at)
            if match is None:return None
            end=text.find(']'+match[1]+']',at+len(match[0]))
            require(end>=0,'unterminated opaque Lua long string or comment')
            return end+len(match[1])+2
        while self.position<len(text):
            char=text[self.position]
            if text.startswith('--',self.position):
                end=long_string(self.position+2)
                if end is None:
                    end=text.find('\n',self.position+2);end=len(text) if end<0 else end+1
                self.position=end;continue
            if char in ('"',"'"):
                quote=char;self.position+=1
                while self.position<len(text) and text[self.position]!=quote:
                    self.position+=2 if text[self.position]=='\\' else 1
                require(self.position<len(text),'unterminated opaque Lua quote')
                self.position+=1;continue
            if char=='[':
                end=long_string(self.position)
                if end is not None:self.position=end;continue
            if char=='{':depth+=1;require(depth<=128,'opaque Lua table nesting exceeds bound')
            elif char=='}':
                depth-=1
                if depth==0:
                    body=text[start:self.position].encode('utf-8');self.position+=1
                    return OpaqueLua(hashlib.sha256(body).hexdigest(),len(body))
            self.position+=1
        require(False,'unterminated opaque Lua block')


def document(value):
    reader=Reader(value);includes=[]
    def block(depth=0):
        require(depth<=128,'public nginx nesting exceeds bound')
        nodes=[];args=[];start=None
        while (token:=reader.next()) is not None:
            kind,word,begin,end=token
            if kind=='word':
                if start is None:start=begin
                args.append(word);continue
            if word=='}':
                require(depth>0 and not args,'invalid public nginx block')
                return nodes
            require(bool(args),'invalid public nginx directive')
            children=None
            if word=='{':
                if args[0].endswith('_by_lua_block'):
                    require(args[0]=='set_by_lua_block' and len(args)==2 and args[1].startswith('$'),
                            'unreviewed Lua block directive')
                    children=reader.lua_body()
                else:children=block(depth+1)
            elif args[0]=='include':includes.append((start,end,tuple(args)))
            nodes.append((tuple(args),children));args=[];start=None
        require(depth==0 and not args,'incomplete public nginx configuration')
        return nodes
    return block(),includes


def parse(value):return document(value)[0]
