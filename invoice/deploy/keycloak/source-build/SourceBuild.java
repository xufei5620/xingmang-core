import java.io.ByteArrayInputStream;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.HexFormat;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Properties;
import java.util.TreeMap;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import java.util.zip.ZipEntry;
import java.util.zip.ZipFile;
import java.util.zip.ZipOutputStream;
import javax.xml.XMLConstants;
import javax.xml.parsers.DocumentBuilderFactory;
import org.w3c.dom.Document;
import org.w3c.dom.Element;
import org.w3c.dom.Node;

/** JDK source-file-mode helper; no third-party build or validation dependency. */
public class SourceBuild {
    record Coordinate(String artifact, String type, String classifier) {
        @Override public String toString() { return "io.netty:" + artifact + ":" + type + ":" + classifier; }
    }
    record JarProof(String path, String artifact, String version, String sha256, boolean runtime) {}
    @FunctionalInterface interface CheckedAction { void run() throws Exception; }

    public static void main(String[] args) throws Exception {
        require(args.length > 0, "Expected self-test, prepare, or verify");
        switch (args[0]) {
            case "self-test" -> { require(args.length == 1, "self-test takes no arguments"); selfTest(); }
            case "prepare" -> {
                require(args.length == 5, "prepare: lock downloads source audit");
                prepare(Path.of(args[1]), Path.of(args[2]), Path.of(args[3]), Path.of(args[4]));
            }
            case "verify" -> {
                require(args.length == 8, "verify: lock bom effective-root effective-server distribution archive audit");
                verify(Path.of(args[1]), Path.of(args[2]), Path.of(args[3]), Path.of(args[4]),
                    Path.of(args[5]), Path.of(args[6]), Path.of(args[7]));
            }
            default -> throw new IllegalArgumentException("Unknown operation: " + args[0]);
        }
    }

    static void prepare(Path lockPath, Path downloads, Path source, Path audit) throws Exception {
        Properties lock = properties(lockPath);
        checkHash(downloads.resolve("keycloak-source.tar.gz"), required(lock, "keycloak.source.sha256"));
        checkHash(downloads.resolve("maven.tar.gz"), required(lock, "maven.sha256"));
        Map<Coordinate, String> bom = lockedBom(downloads.resolve("netty-bom.pom"), lock);
        Path pom = source.resolve("pom.xml");
        byte[] original = Files.readAllBytes(pom);
        require(value(xml(original).getDocumentElement(), "version", "").equals(required(lock, "keycloak.version")),
            "Source project version differs from the lock");
        byte[] patched = patchPom(original, required(lock, "netty.version"));
        Files.createDirectories(audit);
        Files.write(audit.resolve("source-pom.before.xml"), original);
        Files.write(audit.resolve("source-pom.after.xml"), patched);
        Files.write(pom, patched);
        Files.copy(lockPath, audit.resolve("inputs.properties"));
        Files.copy(downloads.resolve("netty-bom.pom"), audit.resolve("netty-bom.pom"));
        write(audit.resolve("source-pom.patch"), bomImport(required(lock, "netty.version"), "\n") + "\n");
        write(audit.resolve("source-inputs.sha256"),
            sha256(downloads.resolve("keycloak-source.tar.gz")) + "  keycloak-source.tar.gz\n" +
            sha256(downloads.resolve("maven.tar.gz")) + "  maven.tar.gz\n" +
            sha256(downloads.resolve("netty-bom.pom")) + "  netty-bom.pom\n" +
            digest(original) + "  source-pom.before.xml\n" + digest(patched) + "  source-pom.after.xml\n");
        StringBuilder managed = new StringBuilder("coordinate\tversion\n");
        bom.forEach((coordinate, version) -> managed.append(coordinate).append('\t').append(version).append('\n'));
        write(audit.resolve("netty-managed.tsv"), managed.toString());
        System.out.println("Prepared one first-priority BOM import; all other source POM bytes preserved");
    }

    static byte[] patchPom(byte[] original, String version) throws Exception {
        Element root = xml(original).getDocumentElement();
        Element dependencies = child(child(root, "dependencyManagement"), "dependencies");
        List<Element> entries = children(dependencies, "dependency");
        require(!entries.isEmpty(), "Source dependencyManagement is empty");
        require(entries.stream().noneMatch(d -> value(d, "groupId", "").equals("io.netty")),
            "Source already contains direct Netty management; refusing duplicate/ambiguous patch");
        require(value(entries.get(0), "artifactId", "").equals("infinispan-bom"),
            "Source BOM order changed: expected Infinispan first");
        String text = new String(original, StandardCharsets.UTF_8);
        require(Arrays.equals(original, text.getBytes(StandardCharsets.UTF_8)), "POM must be valid UTF-8");
        Matcher match = Pattern.compile("<dependencyManagement>\\s*<dependencies>").matcher(text);
        require(match.find(), "Missing literal root dependencyManagement insertion point");
        int insertionAt = match.end();
        require(!match.find(), "Ambiguous dependencyManagement insertion points");
        String newline = text.contains("\r\n") ? "\r\n" : "\n";
        String insertion = bomImport(version, newline);
        String patched = text.substring(0, insertionAt) + insertion + text.substring(insertionAt);
        List<Element> after = children(child(child(xml(patched.getBytes(StandardCharsets.UTF_8))
            .getDocumentElement(), "dependencyManagement"), "dependencies"), "dependency");
        require(after.size() == entries.size() + 1 && value(after.get(0), "artifactId", "").equals("netty-bom")
            && value(after.get(0), "version", "").equals(version)
            && value(after.get(0), "scope", "").equals("import")
            && value(after.get(0), "type", "").equals("pom"), "Patched BOM must have first priority");
        require((patched.substring(0, insertionAt) + patched.substring(insertionAt + insertion.length())).equals(text),
            "Patch changed original source bytes");
        return patched.getBytes(StandardCharsets.UTF_8);
    }

    static String bomImport(String version, String newline) {
        return newline + "            <dependency>" + newline +
            "                <groupId>io.netty</groupId>" + newline +
            "                <artifactId>netty-bom</artifactId>" + newline +
            "                <version>" + version + "</version>" + newline +
            "                <type>pom</type>" + newline +
            "                <scope>import</scope>" + newline +
            "            </dependency>";
    }

    static Map<Coordinate, String> lockedBom(Path bomPath, Properties lock) throws Exception {
        checkHash(bomPath, required(lock, "netty.bom.sha256"));
        Document document = xml(Files.readAllBytes(bomPath));
        Element root = document.getDocumentElement();
        require(value(root, "groupId", "").equals("io.netty") && value(root, "artifactId", "").equals("netty-bom")
            && value(root, "version", "").equals(required(lock, "netty.version")), "Unexpected BOM identity");
        Map<Coordinate, String> expected = managed(document);
        require(expected.size() == Integer.parseInt(required(lock, "netty.bom.coordinates")), "BOM coordinate count changed");
        for (var entry : expected.entrySet()) {
            String familyVersion = required(lock, entry.getKey().artifact().startsWith("netty-tcnative")
                ? "netty.tcnative.version" : "netty.version");
            require(entry.getValue().equals(familyVersion), "Unexpected locked BOM version: " + entry);
        }
        return expected;
    }

    static Map<Coordinate, String> managed(Document document) {
        Element root = document.getDocumentElement();
        Map<String, String> variables = new LinkedHashMap<>();
        variables.put("project.version", value(root, "version", ""));
        variables.put("pom.version", value(root, "version", ""));
        List<Element> propertyElements = children(root, "properties");
        if (!propertyElements.isEmpty()) {
            for (Element item : elements(propertyElements.get(0))) variables.put(item.getLocalName(), item.getTextContent().trim());
        }
        Map<Coordinate, String> result = new TreeMap<>(Comparator.comparing(Coordinate::toString));
        for (Element dependency : children(child(child(root, "dependencyManagement"), "dependencies"), "dependency")) {
            if (!resolve(value(dependency, "groupId", ""), variables).equals("io.netty")) continue;
            Coordinate key = new Coordinate(resolve(value(dependency, "artifactId", ""), variables),
                resolve(value(dependency, "type", "jar"), variables), resolve(value(dependency, "classifier", ""), variables));
            String version = resolve(value(dependency, "version", ""), variables);
            require(!version.isBlank(), "Missing managed version: " + key);
            require(result.putIfAbsent(key, version) == null, "Duplicate managed coordinate: " + key);
        }
        return result;
    }

    static String resolve(String value, Map<String, String> variables) {
        for (int pass = 0; value.contains("${") && pass < 10; pass++) {
            Matcher matcher = Pattern.compile("\\$\\{([^}]+)}").matcher(value);
            StringBuffer output = new StringBuffer();
            while (matcher.find()) {
                String replacement = variables.get(matcher.group(1));
                require(replacement != null, "Unresolved POM property: " + matcher.group(1));
                matcher.appendReplacement(output, Matcher.quoteReplacement(replacement));
            }
            matcher.appendTail(output);
            value = output.toString();
        }
        require(!value.contains("${"), "Cyclic/unresolved POM property");
        return value;
    }

    static void verifyManaged(Map<Coordinate, String> expected, Document actual, String label) {
        Map<Coordinate, String> found = managed(actual);
        require(!found.isEmpty(), label + " has no Netty management");
        for (var entry : expected.entrySet()) {
            require(entry.getValue().equals(found.get(entry.getKey())),
                label + " wrong or missing managed version: " + entry.getKey() + " expected " + entry.getValue()
                + " found " + found.get(entry.getKey()));
        }
        require(found.keySet().equals(expected.keySet()), label + " has Netty coordinates absent from the locked BOM");
    }

    static void verifyTree(Map<Coordinate, String> expected, String tree) {
        boolean handler = false;
        for (String line : tree.split("\\R")) {
            if (line.contains("omitted for ")) continue; // These are inactive requests, not resolved dependencies.
            Matcher matcher = Pattern.compile("\\bio\\.netty:([^\\s()]+)").matcher(line);
            if (!matcher.find()) continue;
            String[] fields = matcher.group(1).split(":", -1);
            require(fields.length == 4 || fields.length == 5, "Unrecognized resolved Netty tree coordinate: " + line);
            Coordinate coordinate = new Coordinate(fields[0], fields[1], fields.length == 5 ? fields[2] : "");
            String version = fields[fields.length - 2];
            require(version.equals(expected.get(coordinate)), "Resolved dependency differs from locked BOM: " + line);
            if (coordinate.artifact().equals("netty-handler")) handler = true;
        }
        require(handler, "Resolved tree is missing netty-handler");
    }

    static List<JarProof> inspectJars(Path distribution, Map<Coordinate, String> expected) throws Exception {
        Map<String, String> artifactVersions = new TreeMap<>();
        expected.forEach((coordinate, version) -> {
            require(coordinate.type().equals("jar"), "Unsupported BOM artifact type: " + coordinate);
            String previous = artifactVersions.putIfAbsent(coordinate.artifact(), version);
            require(previous == null || previous.equals(version), "Classifier-specific versions require explicit JAR resolution");
        });
        List<Path> files;
        try (var walk = Files.walk(distribution)) { files = walk.sorted().toList(); }
        List<JarProof> result = new ArrayList<>();
        boolean handler = false;
        for (Path path : files) {
            require(!Files.isSymbolicLink(path), "Distribution contains an unverified symbolic link: " + path);
            if (!Files.isRegularFile(path) || !path.toString().endsWith(".jar")) continue;
            String relative = distribution.relativize(path).toString().replace('\\', '/');
            String name = path.getFileName().toString();
            boolean namedNetty = name.startsWith("io.netty.netty-");
            boolean runtime = relative.startsWith("lib/");
            int metadataCount = 0;
            try (ZipFile zip = new ZipFile(path.toFile())) {
                for (ZipEntry entry : zip.stream().toList()) {
                    if (!entry.getName().startsWith("META-INF/maven/io.netty/") || !entry.getName().endsWith("/pom.properties")) continue;
                    Properties metadata = new Properties();
                    try (InputStream input = zip.getInputStream(entry)) { metadata.load(input); }
                    require(required(metadata, "groupId").equals("io.netty"), "JAR metadata group mismatch: " + relative);
                    String artifact = required(metadata, "artifactId");
                    String version = required(metadata, "version");
                    require(version.equals(artifactVersions.get(artifact)), "Wrong Netty JAR version: " + relative + " " + artifact + "=" + version);
                    require(!name.contains("4.1.136.Final"), "Old Netty filename remains: " + relative);
                    if (runtime) require(namedNetty && name.startsWith("io.netty." + artifact + "-"),
                        "Runtime Netty JAR name cannot be independently enumerated: " + relative);
                    metadataCount++;
                    result.add(new JarProof(relative, artifact, version, sha256(path), runtime));
                    if (runtime && artifact.equals("netty-handler")) handler = true;
                }
            }
            require(!namedNetty || metadataCount == 1, "Missing or ambiguous Netty JAR pom.properties: " + relative);
            require(!namedNetty || runtime || relative.startsWith("bin/client/"), "Unaccounted Netty JAR outside runtime/client: " + relative);
        }
        require(handler, "Distribution runtime is missing netty-handler");
        return result.stream().sorted(Comparator.comparing(JarProof::path).thenComparing(JarProof::artifact)).toList();
    }

    static void verify(Path lockPath, Path bomPath, Path effectiveRoot, Path effectiveServer,
            Path distribution, Path archive, Path audit) throws Exception {
        Properties lock = properties(lockPath);
        Map<Coordinate, String> expected = lockedBom(bomPath, lock);
        verifyManaged(expected, xml(Files.readAllBytes(effectiveRoot)), "root");
        verifyManaged(expected, xml(Files.readAllBytes(effectiveServer)), "server");
        Path tree = audit.resolve("netty-dependency-tree.txt");
        verifyTree(expected, Files.readString(tree));
        require(Files.isRegularFile(distribution.resolve("bin/kc.sh")) && Files.isDirectory(distribution.resolve("lib/quarkus")),
            "Expected complete unpacked Keycloak distribution");
        require(archive.getFileName().toString().equals("keycloak-" + required(lock, "keycloak.version") + ".tar.gz")
            && Files.size(archive) > 0, "Unexpected or empty distribution archive");
        List<JarProof> jars = inspectJars(distribution, expected);
        StringBuilder hashes = new StringBuilder();
        StringBuilder paths = new StringBuilder();
        List<String> inventory = new ArrayList<>();
        for (JarProof jar : jars) {
            inventory.add("  {\"path\":" + json(jar.path()) + ",\"artifactId\":" + json(jar.artifact())
                + ",\"version\":" + json(jar.version()) + ",\"sha256\":" + json(jar.sha256())
                + ",\"retainedRuntime\":" + jar.runtime() + "}");
            if (jar.runtime()) {
                require(!jar.path().contains("\n") && !jar.path().contains("\r") && !jar.path().contains("\\"), "Unsafe manifest path");
                hashes.append(jar.sha256()).append("  ").append(jar.path()).append('\n');
                paths.append(jar.path()).append('\n');
            }
        }
        write(audit.resolve("netty-jars.json"), "[\n" + String.join(",\n", inventory) + "\n]\n");
        write(audit.resolve("netty-runtime.sha256"), hashes.toString());
        write(audit.resolve("netty-runtime.paths"), paths.toString());
        write(audit.resolve("distribution.sha256"), sha256(archive) + "  " + archive.getFileName() + "\n");
        Map<String, String> proof = new LinkedHashMap<>();
        proof.put("schemaVersion", "1");
        proof.put("keycloakVersion", required(lock, "keycloak.version"));
        proof.put("sourceCommit", required(lock, "keycloak.source.commit"));
        proof.put("sourceArchiveSha256", required(lock, "keycloak.source.sha256"));
        proof.put("builderImage", required(lock, "builder.image"));
        proof.put("runtimeBaseImage", required(lock, "keycloak.base.image"));
        proof.put("nettyBomVersion", required(lock, "netty.version"));
        proof.put("nettyTcnativeVersion", required(lock, "netty.tcnative.version"));
        proof.put("nettyBomSha256", required(lock, "netty.bom.sha256"));
        proof.put("managedCoordinateCount", Integer.toString(expected.size()));
        proof.put("distributionSha256", sha256(archive));
        proof.put("sourcePomBeforeSha256", sha256(audit.resolve("source-pom.before.xml")));
        proof.put("sourcePomAfterSha256", sha256(audit.resolve("source-pom.after.xml")));
        proof.put("sourcePomPatchSha256", sha256(audit.resolve("source-pom.patch")));
        proof.put("effectiveRootSha256", sha256(effectiveRoot));
        proof.put("effectiveServerSha256", sha256(effectiveServer));
        proof.put("dependencyTreeSha256", sha256(tree));
        proof.put("toolchainSha256", sha256(audit.resolve("toolchain.txt")));
        proof.put("runtimeManifestSha256", sha256(audit.resolve("netty-runtime.sha256")));
        List<String> fields = new ArrayList<>();
        proof.forEach((key, value) -> fields.add("  " + json(key) + ": " + json(value)));
        write(audit.resolve("source-build.json"), "{\n" + String.join(",\n", fields) + "\n}\n");
        System.out.println("Verified " + expected.size() + " managed coordinates and " + jars.size() + " distributed Netty metadata entries");
    }

    static void selfTest() throws Exception {
        String original = "<project xmlns=\"http://maven.apache.org/POM/4.0.0\"><version>26.7.2</version>\r\n"
            + "<dependencyManagement><dependencies><dependency><groupId>org.infinispan</groupId>"
            + "<artifactId>infinispan-bom</artifactId><version>16.0.14</version><type>pom</type><scope>import</scope>"
            + "</dependency><dependency><groupId>io.quarkus.platform</groupId><artifactId>quarkus-bom</artifactId>"
            + "<version>3.33.3.1</version><type>pom</type><scope>import</scope></dependency></dependencies></dependencyManagement></project>";
        byte[] patched = patchPom(original.getBytes(StandardCharsets.UTF_8), "4.1.137.Final");
        String patchedText = new String(patched, StandardCharsets.UTF_8);
        require(patchedText.replace(bomImport("4.1.137.Final", "\r\n"), "").equals(original), "Self-test: original POM bytes changed");
        require(patchedText.indexOf("netty-bom") < patchedText.indexOf("infinispan-bom"), "Self-test: BOM lost priority");
        reject("duplicate BOM", () -> patchPom(patched, "4.1.137.Final"));
        Map<Coordinate, String> expected = new TreeMap<>(Comparator.comparing(Coordinate::toString));
        expected.put(new Coordinate("netty-handler", "jar", ""), "4.1.137.Final");
        expected.put(new Coordinate("netty-tcnative", "jar", "linux-x86_64"), "2.0.81.Final");
        String effective = "<project xmlns=\"http://maven.apache.org/POM/4.0.0\"><version>1</version><dependencyManagement><dependencies>"
            + dependency("netty-handler", "4.1.137.Final", "") + dependency("netty-tcnative", "2.0.81.Final", "linux-x86_64")
            + "</dependencies></dependencyManagement></project>";
        verifyManaged(expected, xml(effective.getBytes(StandardCharsets.UTF_8)), "self-test");
        reject("old effective version", () -> verifyManaged(expected, xml(effective.replace("4.1.137.Final", "4.1.136.Final").getBytes(StandardCharsets.UTF_8)), "self-test"));
        reject("wrong classifier", () -> verifyManaged(expected, xml(effective.replace("linux-x86_64", "linux-aarch_64").getBytes(StandardCharsets.UTF_8)), "self-test"));
        verifyTree(expected, "+- io.netty:netty-handler:jar:4.1.137.Final:compile\n");
        reject("old resolved version", () -> verifyTree(expected, "+- io.netty:netty-handler:jar:4.1.136.Final:compile\n"));
        Path fixture = Files.createTempDirectory("keycloak-source-build-self-test-");
        try {
            Path runtime = Files.createDirectories(fixture.resolve("lib/lib/main"));
            Path jar = runtime.resolve("io.netty.netty-handler-4.1.137.Final.jar");
            testJar(jar, "4.1.137.Final");
            require(inspectJars(fixture, expected).size() == 1, "Self-test: correct JAR rejected");
            testJar(jar, "4.1.136.Final");
            reject("old JAR metadata", () -> inspectJars(fixture, expected));
        } finally {
            try (var walk = Files.walk(fixture)) {
                for (Path path : walk.sorted(Comparator.reverseOrder()).toList()) Files.delete(path);
            }
        }
        System.out.println("PASS source-build self-test: patch priority/byte preservation, duplicate rejection, effective versions/classifiers, resolved tree, JAR versions");
    }

    static String dependency(String artifact, String version, String classifier) {
        return "<dependency><groupId>io.netty</groupId><artifactId>" + artifact + "</artifactId><version>" + version
            + "</version>" + (classifier.isEmpty() ? "" : "<classifier>" + classifier + "</classifier>") + "</dependency>";
    }
    static void testJar(Path path, String version) throws Exception {
        try (ZipOutputStream zip = new ZipOutputStream(Files.newOutputStream(path))) {
            zip.putNextEntry(new ZipEntry("META-INF/maven/io.netty/netty-handler/pom.properties"));
            zip.write(("groupId=io.netty\nartifactId=netty-handler\nversion=" + version + "\n").getBytes(StandardCharsets.UTF_8));
            zip.closeEntry();
        }
    }
    static void reject(String label, CheckedAction action) throws Exception {
        boolean rejected = false;
        try { action.run(); } catch (IllegalStateException expected) { rejected = true; }
        require(rejected, "Self-test accepted " + label);
    }
    static Document xml(byte[] data) throws Exception {
        var factory = DocumentBuilderFactory.newInstance();
        factory.setNamespaceAware(true);
        factory.setFeature("http://apache.org/xml/features/disallow-doctype-decl", true);
        factory.setFeature("http://xml.org/sax/features/external-general-entities", false);
        factory.setFeature("http://xml.org/sax/features/external-parameter-entities", false);
        factory.setAttribute(XMLConstants.ACCESS_EXTERNAL_DTD, "");
        factory.setAttribute(XMLConstants.ACCESS_EXTERNAL_SCHEMA, "");
        return factory.newDocumentBuilder().parse(new ByteArrayInputStream(data));
    }
    static List<Element> elements(Element parent) {
        List<Element> result = new ArrayList<>();
        for (Node node = parent.getFirstChild(); node != null; node = node.getNextSibling()) if (node instanceof Element element) result.add(element);
        return result;
    }
    static List<Element> children(Element parent, String name) {
        return elements(parent).stream().filter(element -> name.equals(element.getLocalName())).toList();
    }
    static Element child(Element parent, String name) {
        List<Element> matches = children(parent, name);
        require(matches.size() == 1, "Expected one direct " + name + " under " + parent.getLocalName());
        return matches.get(0);
    }
    static String value(Element parent, String name, String fallback) {
        List<Element> matches = children(parent, name);
        require(matches.size() <= 1, "Duplicate " + name + " under " + parent.getLocalName());
        return matches.isEmpty() ? fallback : matches.get(0).getTextContent().trim();
    }
    static Properties properties(Path path) throws Exception {
        Properties result = new Properties();
        try (InputStream input = Files.newInputStream(path)) { result.load(input); }
        return result;
    }
    static String required(Properties properties, String key) {
        String value = properties.getProperty(key);
        require(value != null && !value.isBlank(), "Missing property: " + key);
        return value;
    }
    static String sha256(Path path) throws Exception {
        MessageDigest digest = MessageDigest.getInstance("SHA-256");
        try (InputStream input = Files.newInputStream(path)) {
            byte[] buffer = new byte[65536];
            for (int read; (read = input.read(buffer)) >= 0;) if (read > 0) digest.update(buffer, 0, read);
        }
        return HexFormat.of().formatHex(digest.digest());
    }
    static String digest(byte[] value) throws Exception { return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(value)); }
    static void checkHash(Path path, String expected) throws Exception { require(sha256(path).equals(expected), "SHA256 mismatch: " + path); }
    static void write(Path path, String text) throws Exception { Files.writeString(path, text, StandardCharsets.UTF_8); }
    static String json(String text) {
        StringBuilder result = new StringBuilder("\"");
        for (char c : text.toCharArray()) {
            if (c == '"' || c == '\\') result.append('\\').append(c);
            else if (c < 32) result.append(String.format("\\u%04x", (int)c));
            else result.append(c);
        }
        return result.append('"').toString();
    }
    static void require(boolean condition, String message) { if (!condition) throw new IllegalStateException(message); }
}
