package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dsnet/compress/brotli"
	"github.com/khlieng/dispatch/assets"
	"github.com/spf13/viper"
)

const longCacheControl = "public, max-age=31536000, immutable"
const disabledCacheControl = "no-cache, no-store, must-revalidate"

type File struct {
	Path         string
	Asset        string
	GzipAsset    []byte
	Hash         string
	ContentType  string
	CacheControl string
	Compressed   bool
}

type h2PushAsset struct {
	path string
	hash string
}

func newH2PushAsset(name string) h2PushAsset {
	return h2PushAsset{
		path: "/" + name,
		hash: strings.Split(name, ".")[1],
	}
}

var (
	files []*File

	indexStylesheet      string
	indexScripts         []string
	inlineScript         string
	inlineScriptSha256   string
	inlineScriptSW       string
	inlineScriptSWSha256 string
	serviceWorker        []byte

	h2PushAssets      []h2PushAsset
	h2PushCookieValue string

	contentTypes = map[string]string{
		".js":    "text/javascript",
		".css":   "text/css",
		".woff2": "font/woff2",
		".woff":  "application/font-woff",
		".ttf":   "application/x-font-ttf",
	}

	hstsHeader string
	cspEnabled bool
)

func (d *Dispatch) initFileServer() {
	if viper.GetBool("dev") {
		indexScripts = []string{"bundle.js"}
	} else {
		data, err := assets.Asset("asset-manifest.json")
		if err != nil {
			log.Fatal(err)
		}

		manifest := map[string]string{}
		err = json.Unmarshal(data, &manifest)
		if err != nil {
			log.Fatal(err)
		}

		bootloader := decompressedAsset(manifest["boot.js"])
		runtime := decompressedAsset(manifest["runtime.js"])

		inlineScript = string(runtime)
		inlineScriptSW = string(bootloader) + string(runtime)

		hash := sha256.New()
		hash.Write(runtime)
		inlineScriptSha256 = base64.StdEncoding.EncodeToString(hash.Sum(nil))

		hash.Reset()
		hash.Write(bootloader)
		hash.Write(runtime)
		inlineScriptSWSha256 = base64.StdEncoding.EncodeToString(hash.Sum(nil))

		indexStylesheet = manifest["main.css"]
		indexScripts = []string{
			manifest["vendors~main.js"],
			manifest["main.js"],
		}

		h2PushAssets = []h2PushAsset{
			newH2PushAsset(indexStylesheet),
			newH2PushAsset(indexScripts[0]),
			newH2PushAsset(indexScripts[1]),
		}

		for _, asset := range h2PushAssets {
			h2PushCookieValue += asset.hash
		}

		ignoreAssets := []string{
			manifest["runtime.js"],
			manifest["boot.js"],
			"sw.js",
		}

		for _, assetPath := range manifest {
			for _, ignored := range ignoreAssets {
				if assetPath == ignored {
					continue
				}
			}

			file := &File{
				Path:         assetPath,
				Asset:        assetPath + ".br",
				ContentType:  contentTypes[filepath.Ext(assetPath)],
				CacheControl: longCacheControl,
				Compressed:   true,
			}

			files = append(files, file)
		}

		fonts, err := assets.AssetDir("font")
		if err != nil {
			log.Fatal(err)
		}

		for _, font := range fonts {
			p := strings.TrimSuffix(font, ".br")

			file := &File{
				Path:         path.Join("font", p),
				Asset:        path.Join("font", font),
				ContentType:  contentTypes[filepath.Ext(p)],
				CacheControl: longCacheControl,
				Compressed:   strings.HasSuffix(font, ".br"),
			}

			files = append(files, file)
		}

		for _, file := range files {
			if file.Compressed {
				data, err := assets.Asset(file.Asset)
				if err != nil {
					log.Fatal(err)
				}

				file.GzipAsset = gzipAsset(data)
			}
		}

		serviceWorker = decompressedAsset("sw.js")
		hash.Reset()
		IndexTemplate(hash, nil, indexStylesheet, inlineScriptSW, indexScripts)
		indexHash := base64.StdEncoding.EncodeToString(hash.Sum(nil))

		serviceWorker = append(serviceWorker, []byte(`
workbox.precaching.precacheAndRoute([{
	revision: '`+indexHash+`',
	url: '/?sw'
}]);
workbox.routing.registerNavigationRoute('/?sw');`)...)

		if viper.GetBool("https.hsts.enabled") && viper.GetBool("https.enabled") {
			hstsHeader = "max-age=" + viper.GetString("https.hsts.max_age")

			if viper.GetBool("https.hsts.include_subdomains") {
				hstsHeader += "; includeSubDomains"
			}
			if viper.GetBool("https.hsts.preload") {
				hstsHeader += "; preload"
			}
		}

		cspEnabled = true
	}
}

func decompressAsset(data []byte) []byte {
	br, err := brotli.NewReader(bytes.NewReader(data), nil)
	if err != nil {
		log.Fatal(err)
	}

	buf := &bytes.Buffer{}
	io.Copy(buf, br)
	return buf.Bytes()
}

func decompressedAsset(name string) []byte {
	asset, err := assets.Asset(name + ".br")
	if err != nil {
		log.Fatal(err)
	}
	return decompressAsset(asset)
}

func gzipAsset(data []byte) []byte {
	br, err := brotli.NewReader(bytes.NewReader(data), nil)
	if err != nil {
		log.Fatal(err)
	}

	buf := &bytes.Buffer{}
	gzw, err := gzip.NewWriterLevel(buf, gzip.BestCompression)
	if err != nil {
		log.Fatal(err)
	}

	io.Copy(gzw, br)
	gzw.Close()
	return buf.Bytes()
}

func (d *Dispatch) serveFiles(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		d.serveIndex(w, r)
		return
	}

	if r.URL.Path == "/sw.js" {
		w.Header().Set("Cache-Control", disabledCacheControl)
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Content-Length", strconv.Itoa(len(serviceWorker)))
		w.Write(serviceWorker)
		return
	}

	for _, file := range files {
		if strings.HasSuffix(r.URL.Path, file.Path) {
			d.serveFile(w, r, file)
			return
		}
	}

	d.serveIndex(w, r)
}

func (d *Dispatch) serveIndex(w http.ResponseWriter, r *http.Request) {
	state := d.handleAuth(w, r, false)

	_, sw := r.URL.Query()["sw"]

	if cspEnabled {
		var wsSrc string
		if r.TLS != nil {
			wsSrc = "wss://" + r.Host
		} else {
			wsSrc = "ws://" + r.Host
		}

		inlineSha := inlineScriptSha256
		if sw {
			inlineSha = inlineScriptSWSha256
		}

		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'sha256-"+inlineSha+"'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src data:; connect-src 'self' "+wsSrc)
	}

	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", disabledCacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "deny")
	w.Header().Set("X-XSS-Protection", "1; mode=block")

	if hstsHeader != "" {
		w.Header().Set("Strict-Transport-Security", hstsHeader)
	}

	if pusher, ok := w.(http.Pusher); ok {
		options := &http.PushOptions{
			Header: http.Header{
				"Accept-Encoding": r.Header["Accept-Encoding"],
			},
		}
		cookie, err := r.Cookie("push")
		if err != nil {
			for _, asset := range h2PushAssets {
				pusher.Push(asset.path, options)
			}

			setPushCookie(w, r)
		} else {
			pushed := false

			for i, asset := range h2PushAssets {
				if len(cookie.Value) >= (i+1)*8 &&
					asset.hash != cookie.Value[i*8:(i+1)*8] {
					pusher.Push(asset.path, options)
					pushed = true
				}
			}

			if pushed {
				setPushCookie(w, r)
			}
		}
	}

	var data *indexData
	if !sw {
		data = getIndexData(r, state)
	}

	inline := inlineScript
	if sw {
		inline = inlineScriptSW
	}

	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")

		gzw := gzip.NewWriter(w)
		IndexTemplate(gzw, data, indexStylesheet, inline, indexScripts)
		gzw.Close()
	} else {
		IndexTemplate(w, data, indexStylesheet, inline, indexScripts)
	}
}

func setPushCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "push",
		Value:    h2PushCookieValue,
		Path:     "/",
		Expires:  time.Now().AddDate(1, 0, 0),
		HttpOnly: true,
		Secure:   r.TLS != nil,
	})
}

func (d *Dispatch) serveFile(w http.ResponseWriter, r *http.Request, file *File) {
	data, err := assets.Asset(file.Asset)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	if file.CacheControl != "" {
		w.Header().Set("Cache-Control", file.CacheControl)
	}

	w.Header().Set("Content-Type", file.ContentType)

	if file.Compressed && strings.Contains(r.Header.Get("Accept-Encoding"), "br") {
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	} else if file.Compressed && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(file.GzipAsset)))
		w.Write(file.GzipAsset)
	} else if !file.Compressed {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	} else {
		gzr, err := gzip.NewReader(bytes.NewReader(file.GzipAsset))
		buf, err := ioutil.ReadAll(gzr)
		if err != nil {
			http.Error(w, "", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
		w.Write(buf)
	}
}
