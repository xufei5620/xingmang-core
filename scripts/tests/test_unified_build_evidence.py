import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('gate',Path(__file__).resolve().parents[1]/'unified-service.py')
gate=importlib.util.module_from_spec(spec);spec.loader.exec_module(gate)
OCI_INDEX='application/vnd.oci.image.index.v1+json'
OCI_MANIFEST='application/vnd.oci.image.manifest.v1+json'
OCI_CONFIG='application/vnd.oci.image.config.v1+json'
OCI_LAYER='application/vnd.oci.image.layer.v1.tar'
REF='example/runtime:test'

def sha(body):return 'sha256:'+hashlib.sha256(body).hexdigest()
def raw(value):return json.dumps(value,separators=(',',':')).encode()
def blob(digest):return 'blobs/sha256/'+digest.split(':')[1]

def sample(oci=True):
    layer=b'synthetic-layer-tar-bytes'
    config=raw({'architecture':'amd64','os':'linux','rootfs':{'type':'layers','diff_ids':[sha(layer)]}})
    configid=sha(config);layerid=sha(layer)
    files={blob(configid):config,blob(layerid):layer}
    docker=[{'Config':blob(configid),'RepoTags':[REF],'Layers':[blob(layerid)]}]
    files['manifest.json']=raw(docker)
    if not oci:return files,configid,configid
    manifest=raw({'schemaVersion':2,'mediaType':OCI_MANIFEST,'config':{'mediaType':OCI_CONFIG,'digest':configid,'size':len(config)},'layers':[{'mediaType':OCI_LAYER,'digest':layerid,'size':len(layer)}]})
    manifestid=sha(manifest);files[blob(manifestid)]=manifest
    index=raw({'schemaVersion':2,'mediaType':OCI_INDEX,'manifests':[{'mediaType':OCI_MANIFEST,'digest':manifestid,'size':len(manifest),'platform':{'os':'linux','architecture':'amd64'}}]})
    imageid=sha(index);files[blob(imageid)]=index
    files['index.json']=raw({'schemaVersion':2,'mediaType':OCI_INDEX,'manifests':[{'mediaType':OCI_INDEX,'digest':imageid,'size':len(index),'annotations':{'io.containerd.image.name':'docker.io/'+REF}}]})
    return files,imageid,configid

def write_tar(path,files,duplicate=None):
    with tarfile.open(path,'w') as archive:
        for name,body in list(files.items())+([duplicate] if duplicate else []):
            member=tarfile.TarInfo(name);member.size=len(body);archive.addfile(member,io.BytesIO(body))

class BuildEvidenceTests(unittest.TestCase):
    def verify(self,files,imageid):
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder)/'image.tar';write_tar(path,files)
            return gate.assert_archive_image(path,REF,imageid)

    def test_classic_config_identity_supported(self):
        files,imageid,config=sample(False)
        self.assertEqual(self.verify(files,imageid)['configDigest'],config)

    def test_containerd_index_identity_proves_chain_to_config(self):
        files,imageid,config=sample()
        try:result=self.verify(files,imageid)
        except gate.GateError as error:self.fail('a valid descriptor chain must be accepted: '+str(error))
        self.assertEqual(result['configDigest'],config);self.assertEqual(result['imageId'],imageid)
        self.assertNotEqual(config,imageid)

    def test_broken_oci_chain_is_rejected(self):
        for corruption in ['root-bytes','root-unreachable','root-annotation','root-size','config-bytes','config-link','missing-layer','layer-bytes','wrong-tag','wrong-image']:
            with self.subTest(corruption=corruption):
                files,imageid,config=sample()
                index=json.loads(files['index.json'])
                if corruption=='root-bytes':files[blob(imageid)]+=b' '
                if corruption=='root-unreachable':index['manifests']=[];files['index.json']=raw(index)
                if corruption=='root-annotation':index['manifests'][0]['annotations']['io.containerd.image.name']='docker.io/other/image:test';files['index.json']=raw(index)
                if corruption=='root-size':index['manifests'][0]['size']+=1;files['index.json']=raw(index)
                if corruption=='config-bytes':files[blob(config)]+=b' '
                if corruption=='config-link':d=json.loads(files['manifest.json']);d[0]['Config']=blob(imageid);files['manifest.json']=raw(d)
                if corruption=='missing-layer':del files[json.loads(files['manifest.json'])[0]['Layers'][0]]
                if corruption=='layer-bytes':files[json.loads(files['manifest.json'])[0]['Layers'][0]]+=b'corruption'
                if corruption=='wrong-tag':d=json.loads(files['manifest.json']);d[0]['RepoTags']=['other/image:test'];files['manifest.json']=raw(d)
                if corruption=='wrong-image':imageid='sha256:'+'f'*64
                with self.assertRaises(gate.GateError):self.verify(files,imageid)

    def test_orphan_valid_blob_is_not_accepted_as_image(self):
        files,imageid,_=sample();orphan=raw({'schemaVersion':2,'mediaType':OCI_MANIFEST,'config':{},'layers':[]})
        files[blob(sha(orphan))]=orphan
        with self.assertRaises(gate.GateError):self.verify(files,sha(orphan))

    def test_oci_config_id_cannot_replace_the_reachable_image_id(self):
        files,_,config=sample()
        with self.assertRaises(gate.GateError):self.verify(files,config)

    def test_oci_pinned_reference_is_bound_without_optional_name_annotation(self):
        files,imageid,_=sample()
        index=json.loads(files['index.json']);index['manifests'][0].pop('annotations');files['index.json']=raw(index)
        docker=json.loads(files['manifest.json']);docker[0]['RepoTags']=None;files['manifest.json']=raw(docker)
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder)/'image.tar';write_tar(path,files)
            gate.assert_archive_image(path,'example/runtime@'+imageid,imageid)
            with self.assertRaises(gate.GateError):gate.assert_archive_image(path,'example/runtime@sha256:'+'f'*64,imageid)

    def test_duplicate_config_member_is_rejected(self):
        files,imageid,config=sample()
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder)/'image.tar';write_tar(path,files,(blob(config),files[blob(config)]))
            with self.assertRaises(gate.GateError):gate.assert_archive_image(path,REF,imageid)

    def test_classic_layer_content_is_bound_to_config(self):
        files,imageid,_=sample(False)
        files[json.loads(files['manifest.json'])[0]['Layers'][0]]+=b'wrong'
        with self.assertRaises(gate.GateError):self.verify(files,imageid)

    def test_buildkit_output_binding_accepts_actual_index_shape_without_config_field(self):
        _,imageid,config=sample()
        metadata={'containerimage.digest':imageid,'containerimage.descriptor':{'digest':imageid,'mediaType':OCI_INDEX},'image.name':REF}
        gate.assert_buildkit_output(metadata,{'imageId':imageid,'configDigest':config},REF)

    def test_buildkit_wrong_output_or_reference_rejected(self):
        _,imageid,config=sample()
        for kind in ['image','descriptor','config','reference','missing']:
            with self.subTest(kind=kind):
                metadata={'containerimage.digest':imageid,'containerimage.descriptor':{'digest':imageid,'mediaType':OCI_INDEX},'image.name':REF}
                if kind=='image':metadata['containerimage.digest']='sha256:'+'f'*64
                if kind=='descriptor':metadata['containerimage.descriptor']['digest']='sha256:'+'f'*64
                if kind=='config':metadata['containerimage.config.digest']='sha256:'+'f'*64
                if kind=='reference':metadata['image.name']='other/image:test'
                if kind=='missing':del metadata['containerimage.digest']
                with self.assertRaises(gate.GateError):gate.assert_buildkit_output(metadata,{'imageId':imageid,'configDigest':config},REF)

    def test_classic_buildkit_requires_output_config_binding(self):
        _,imageid,config=sample(False)
        gate.assert_buildkit_output({'containerimage.digest':'sha256:'+'d'*64,'containerimage.config.digest':config},{'imageId':imageid,'configDigest':config},REF)
        with self.assertRaises(gate.GateError):gate.assert_buildkit_output({'containerimage.digest':'sha256:'+'d'*64},{'imageId':imageid,'configDigest':config},REF)

    def test_builder_must_be_the_explicit_context_local_docker_driver(self):
        good='Name: qualification-local\nDriver: docker\n\nNodes:\nName: qualification-local\nEndpoint: qualification-local\nStatus: running\n'
        self.assertEqual(gate.assert_local_builder(good,'qualification-local')['driver'],'docker')
        for text in [good.replace('Driver: docker','Driver: remote'),good.replace('Endpoint: qualification-local','Endpoint: ssh://prod'),good+'Endpoint: another\n',good.replace('Name: qualification-local','Name: wrong',1)]:
            with self.assertRaises(gate.GateError):gate.assert_local_builder(text,'qualification-local')

    def test_external_builder_environment_cannot_escape_local_context(self):
        env={'BUILDX_BUILDER':'remote','BUILDKIT_HOST':'tcp://remote:1234','DOCKER_HOST':'ssh://prod','DOCKER_CONTEXT':'prod','PATH':'keep'}
        cleaned=gate.local_tool_environment(env)
        self.assertEqual(cleaned.get('PATH'),'keep')
        for name in ['BUILDX_BUILDER','BUILDKIT_HOST','DOCKER_HOST','DOCKER_CONTEXT']:self.assertNotIn(name,cleaned)

    def test_build_arguments_force_reviewed_builder(self):
        args=gate.build_arguments({'dockerfile':'Dockerfile','repository':'example/runtime'},'test',{'gitHead':'a'*40,'inventorySha256':'b'*64},'c'*64,builder='qualification-local')
        self.assertIn('--builder',args);self.assertEqual(args[args.index('--builder')+1],'qualification-local')

if __name__=='__main__':unittest.main()
