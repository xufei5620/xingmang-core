"""Freeze the original loaded include graph; never rewrite a live nginx file."""
import fnmatch
import hashlib
from pathlib import Path, PurePosixPath, PureWindowsPath

from lifecycle import plain_path, require
from host_nginx import parse, public_input
from nginx_syntax import OpaqueLua


def nginx_path(value):
    return PureWindowsPath(value) if len(value)>2 and value[1]==':' else PurePosixPath(value)


def include_path(value, prefix):
    path=nginx_path(value)
    require('..' not in path.parts and '$' not in value and '\n' not in value and '\r' not in value,
            'nginx include path is dynamic or traverses its reviewed prefix')
    return str(path if path.is_absolute() else nginx_path(prefix)/path)


def disk_path(host, runtime, *, exists=True, directory=False):
    if host.driver.config['mode']=='local-synthetic':
        path=PurePosixPath(runtime)
        require(path.is_relative_to(host.container_root),'local public include is outside the reviewed mapped configuration root')
        physical=host.host_root/path.relative_to(host.container_root).as_posix()
    else:
        physical=Path(runtime)
    return plain_path(str(physical),exists=exists,directory=directory)


def directory_state(host, runtime):
    path=disk_path(host,runtime,exists=False,directory=True)
    if not path.exists():return {'exists':False,'entries':[]}
    require(path.is_dir(),'nginx glob parent is not a directory')
    entries=[]
    for item in path.iterdir():
        require(len(entries)<1024,'nginx glob directory exceeds the reviewed member bound')
        linked=item.is_symlink() or (hasattr(item,'is_junction') and item.is_junction())
        entries.append({'name':item.name,'type':'link' if linked else 'directory' if item.is_dir() else 'file' if item.is_file() else 'other'})
    return {'exists':True,'entries':sorted(entries,key=lambda row:row['name'])}


class FrozenLayout:
    def __init__(self,host,loaded):
        self.host=host;self.loaded=loaded;self.main=host.runtime_path(host.main)
        self.prefix=str(nginx_path(self.main).parent);self.targets={host.runtime_path(a) for a,_ in host.rows}
        require(0<len(loaded)<=256 and self.main in loaded,'nginx loaded file inventory exceeds bounds or misses main')
        self.files={};self.globs={};self.directories={};self.expanded_globs={};self.relative=False;self.visits={};self.edge_count=0
        require(sum(len(body.encode('utf-8')) for body in loaded.values())<=8*1024*1024,'nginx loaded public configuration exceeds total bound')
        self.nodes={}
        for runtime,body in loaded.items():
            path=disk_path(host,runtime);require(path.stat().st_size<=1024*1024,'nginx public include exceeds file bound')
            raw,identity=public_input(path)
            require(raw.decode('utf-8').strip()==body,'nginx loaded text differs from the original public file')
            self.files[runtime]={'sha256':hashlib.sha256(raw).hexdigest(),'identity':list(identity)}
            self.nodes[runtime]=parse(body)
        self._visit(self.main,[])
        require(set(self.visits)==set(loaded),'nginx loaded sections do not match the bounded include graph')
        require(all(self.visits.get(path)==1 for path in self.targets),'each reviewed vhost must be included exactly once')
        for members in self.expanded_globs.values():
            require(all(self.visits.get(path)==1 for path in members),'expanded nginx glob overlaps another include reference')
        for path in [host.runtime_path(host.staged),*[host.runtime_path(b) for _,b in host.rows]]:
            require(str(nginx_path(path).parent) not in self.directories,
                    'staged nginx inputs must stay outside watched include directories')
        if self.relative:
            require(host.staged.parent==host.main.parent,'staged main must preserve the real main directory for relative includes')

    def _members(self,pattern):
        path=nginx_path(pattern);parent=str(path.parent);mask=path.name
        require(path.is_absolute() and not any(c in parent for c in '*?[') and '**' not in mask,
                'nginx glob must have one literal absolute parent directory')
        state=directory_state(self.host,parent)
        if parent in self.directories:require(state==self.directories[parent],'nginx directory changed during include graph capture')
        self.directories[parent]=state
        matched={str(path.parent/row['name']) for row in state['entries'] if fnmatch.fnmatchcase(row['name'],mask)}
        require(len(matched)<=256 and matched<=set(self.loaded),'nginx glob members are missing from the actual loaded inventory')
        # -T records first-load order. Expanded members may have only one
        # reference, verified after traversing the graph, so this is unambiguous.
        ordered=[name for name in self.loaded if name in matched]
        self.globs[pattern]=ordered
        return ordered

    def _visit(self,runtime,stack):
        require(runtime in self.nodes and runtime not in stack and len(stack)<32,'unknown or cyclic nginx include')
        self.edge_count+=1;require(self.edge_count<=2048,'nginx include graph exceeds reference bound')
        self.visits[runtime]=self.visits.get(runtime,0)+1
        def walk(nodes):
            for args,children in nodes:
                if args[0]=='include':
                    require(children is None and len(args)==2,'invalid nginx include directive')
                    relative=not nginx_path(args[1]).is_absolute();self.relative=self.relative or relative
                    target=include_path(args[1],self.prefix)
                    if any(c in target for c in '*?['):
                        members=self._members(target)
                        if self.targets.intersection(members):
                            require(runtime==self.main and not relative and nginx_path(target).name=='*.conf',
                                    'reviewed vhost glob must be the original absolute main-level *.conf include')
                            self.expanded_globs[args[1]]=members
                        for member in members:self._visit(member,[*stack,runtime])
                    else:self._visit(target,[*stack,runtime])
                if children is not None and not isinstance(children,OpaqueLua):walk(children)
        walk(self.nodes[runtime])

    def proof(self):
        return {'files':self.files,'globs':self.globs,'directories':self.directories,
                'expanded_globs':self.expanded_globs,'relative_includes':self.relative,'main_prefix':self.prefix}


def verify_layout(host,proof,allowed_vhosts):
    require(set(proof)=={'files','globs','directories','expanded_globs','relative_includes','main_prefix'},'invalid frozen nginx layout proof')
    for runtime,expected in proof['files'].items():
        raw,identity=public_input(disk_path(host,runtime));actual=hashlib.sha256(raw).hexdigest()
        if runtime in allowed_vhosts:
            require(actual in allowed_vhosts[runtime],'reviewed live vhost changed during validation or outside the operation')
            require(list(identity)[2:5]==expected['identity'][2:5],'reviewed live vhost permissions or ownership changed')
        else:
            require(actual==expected['sha256'] and list(identity)[2:5]==expected['identity'][2:5],
                    'frozen nginx main/shared policy file changed during validation')
    for runtime,expected in proof['directories'].items():
        require(directory_state(host,runtime)==expected,'frozen nginx include directory membership changed')


def same_structure(before,after):
    require(set(before['files'])==set(after['files']) and
            all(before[key]==after[key] for key in before if key!='files'),
            'frozen nginx include graph, order or directory membership changed')


def header_nodes(nodes,loaded,prefix):
    references=[0];total=[0]
    def walk(items,scope=None):
        result=[]
        for args,children in items:
            if args[0]=='include':
                require(scope in ('server','location') and children is None and len(args)==2,
                        'only direct server/location header includes are supported')
                target=include_path(args[1],prefix)
                require(not any(c in target for c in '*?[') and target in loaded,'header include must be an original loaded literal file')
                body=loaded[target];references[0]+=1;total[0]+=len(body.encode('utf-8'))
                require(references[0]<=64 and len(body.encode('utf-8'))<=65536 and total[0]<=262144,'header include expansion exceeds bounds')
                fragment=parse(body)
                for head,block in fragment:
                    allowed=(head[0]=='add_header' and (len(head)==3 or (len(head)==4 and head[-1]=='always')) or
                             head[0]=='proxy_set_header' and len(head)==3 or head[0]=='proxy_hide_header' and len(head)==2 or
                             head[0]=='proxy_http_version' and len(head)==2 and head[1] in ('1.0','1.1'))
                    require(block is None and allowed,'header include has an unknown directive, nested include or handler')
                result.extend(fragment)
            else:result.append((args,children if children is None or isinstance(children,OpaqueLua) else walk(children,args[0])))
        return result
    return walk(nodes)
