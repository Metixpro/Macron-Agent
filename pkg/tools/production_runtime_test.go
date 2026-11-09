package tools

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImageDecodersInProductionBuild(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for _, format := range []string{"jpeg", "png"} {
		f, err := os.Create(filepath.Join(dir, "input."+format))
		if err != nil {
			t.Fatal(err)
		}
		if format == "jpeg" {
			err = jpeg.Encode(f, img, nil)
		} else {
			err = png.Encode(f, img)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	source := `package main
import("fmt";"image";"os";_ "github.com/Metixpro/macron-agent/pkg/tools")
func main(){for _,path:=range os.Args[1:]{f,err:=os.Open(path);if err!=nil{panic(err)};config,kind,err:=image.DecodeConfig(f);f.Close();if err!=nil{panic(err)};if config.Width!=800||config.Height!=600{panic("wrong dimensions")};fmt.Println(kind)}}`
	script := filepath.Join(dir, "probe.go")
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", script, filepath.Join(dir, "input.jpeg"), filepath.Join(dir, "input.png"))
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "jpeg\npng\n" {
		t.Fatalf("production-only decoder probe: %v\n%s", err, out)
	}
}

func testSpeechCA(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return path, hex.EncodeToString(sum[:])
}

func TestEdgeSpeechPreservesProxyAndTrustedCertificates(t *testing.T) {
	code, err := scriptFS.ReadFile("scripts/edge_speech.py")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bundle, bundleHash := testSpeechCA(t, dir, "package-root")
	extra, extraHash := testSpeechCA(t, dir, "server-root")
	shim := fmt.Sprintf(`import types,sys,json,hashlib
certifi=types.ModuleType("certifi")
certifi.where=lambda:%q
sys.modules["certifi"]=certifi
edge=types.ModuleType("edge_tts")
communicate=types.ModuleType("edge_tts.communicate")
edge.communicate=communicate
class Speech:
 def __init__(self,text,voice,rate,proxy):
  self.result={"text":text,"voice":voice,"rate":rate,"proxy":proxy}
 async def save(self,path):
  roots={hashlib.sha256(c).hexdigest() for c in communicate._SSL_CTX.get_ca_certs(binary_form=True)}
  assert %q in roots and %q in roots,"configured CA was lost"
  assert communicate._SSL_CTX.check_hostname and communicate._SSL_CTX.verify_mode==2,"TLS verification disabled"
  self.result["filename"]=path
  print(json.dumps(self.result))
edge.Communicate=Speech
sys.modules["edge_tts"]=edge
sys.modules["edge_tts.communicate"]=communicate
`, bundle, bundleHash, extraHash)
	cases := []struct{ name, caKey, proxyKey, noProxy, want string }{
		{"system CA and uppercase proxy", "SSL_CERT_FILE", "HTTPS_PROXY", "", "http://proxy.example:8080"},
		{"custom bundle and lowercase proxy", "REQUESTS_CA_BUNDLE", "https_proxy", "", "http://proxy.example:8080"},
		{"no-proxy exclusion", "SSL_CERT_FILE", "HTTPS_PROXY", "speech.platform.bing.com", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "python3", "-I", "-c", shim+string(code))
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				low := strings.ToLower(key)
				if strings.Contains(low, "proxy") || key == "SSL_CERT_FILE" || key == "SSL_CERT_DIR" || key == "REQUESTS_CA_BUNDLE" || key == "CURL_CA_BUNDLE" {
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, tc.caKey+"="+extra, tc.proxyKey+"=http://proxy.example:8080", "NO_PROXY="+tc.noProxy)
			cmd.Stdin = strings.NewReader(`{"text":"سلام","voice":"fa-IR-FaridNeural","rate":"+25%","filename":"speech.mp3"}`)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("speech transport: %v\n%s", err, out)
			}
			if !bytes.Contains(out, []byte(`"rate": "+25%"`)) || !bytes.Contains(out, []byte(`"filename": "speech.mp3"`)) {
				t.Fatalf("speech options lost: %s", out)
			}
			expected := `"proxy": null`
			if tc.want != "" {
				expected = fmt.Sprintf(`"proxy": %q`, tc.want)
			}
			if !strings.Contains(string(out), expected) {
				t.Fatalf("proxy: wanted %s, got %s", expected, out)
			}
		})
	}
}
