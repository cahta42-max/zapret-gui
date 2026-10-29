// Package release работает с GitHub Releases: получение информации о последнем
// релизе, скачивание ассета с прогрессом и распаковка zip.
package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	githubAPI     = "https://api.github.com/repos"
	ZapretRepo    = "Flowseal/zapret-discord-youtube"
	TgwsRepo      = "Flowseal/tg-ws-proxy"
	SpoofDPIRepo  = "xvzc/SpoofDPI"
	ZapretMacRepo = "Flowseal/zapret-mac-discord-youtube"
)

// Asset — один файл релиза.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Info — краткая информация о релизе.
type Info struct {
	Tag    string
	Assets []Asset
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

// fetchLatest возвращает информацию о последнем релизе репозитория.
func fetchLatest(ctx context.Context, repo string) (*Info, error) {
	url := fmt.Sprintf("%s/%s/releases/latest", githubAPI, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	info := &Info{Tag: payload.TagName}
	for _, a := range payload.Assets {
		info.Assets = append(info.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return info, nil
}

func findAsset(info *Info, pred func(Asset) bool) (Asset, error) {
	for _, a := range info.Assets {
		if pred(a) {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("подходящий ассет не найден в релизе %s", info.Tag)
}

// LatestZapret — последний релиз zapret (zip-архив).
func LatestZapret(ctx context.Context) (tag string, asset Asset, err error) {
	info, err := fetchLatest(ctx, ZapretRepo)
	if err != nil {
		return "", Asset{}, err
	}
	asset, err = findAsset(info, func(a Asset) bool { return strings.HasSuffix(a.Name, ".zip") })
	return info.Tag, asset, err
}

// ZapretVersions — список тегов релизов zapret, имеющих zip-ассет
// (новейшие первыми).
func ZapretVersions(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("%s/%s/releases?per_page=15", githubAPI, ZapretRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}
	var payload []struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	var tags []string
	for _, rel := range payload {
		for _, a := range rel.Assets {
			if strings.HasSuffix(a.Name, ".zip") {
				tags = append(tags, rel.TagName)
				break
			}
		}
	}
	return tags, nil
}

// ZapretReleaseAsset — zip-ассет конкретного тега релиза zapret.
func ZapretReleaseAsset(ctx context.Context, tag string) (Asset, error) {
	url := fmt.Sprintf("%s/%s/releases/tags/%s", githubAPI, ZapretRepo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Asset{}, err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Asset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Asset{}, fmt.Errorf("релиз %s: %s", tag, resp.Status)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Asset{}, err
	}
	info := &Info{Tag: payload.TagName}
	for _, a := range payload.Assets {
		info.Assets = append(info.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return findAsset(info, func(a Asset) bool { return strings.HasSuffix(a.Name, ".zip") })
}

// LatestZapretMac — последний релиз ZapretMac (macOS-порт zapret:
// движок utunws; универсальный zip).
func LatestZapretMac(ctx context.Context) (tag string, asset Asset, err error) {
	info, err := fetchLatest(ctx, ZapretMacRepo)
	if err != nil {
		return "", Asset{}, err
	}
	asset, err = findAsset(info, func(a Asset) bool {
		return strings.Contains(strings.ToLower(a.Name), "universal") && strings.HasSuffix(a.Name, ".zip")
	})
	return info.Tag, asset, err
}

// ZapretMacVersions — теги релизов ZapretMac (новейшие первыми).
func ZapretMacVersions(ctx context.Context) ([]string, error) {
	return repoTags(ctx, ZapretMacRepo, func(name string) bool {
		return strings.HasSuffix(name, ".zip")
	})
}

// ZapretMacAsset — universal-zip конкретного релиза ZapretMac.
func ZapretMacAsset(ctx context.Context, tag string) (Asset, error) {
	info, err := fetchTag(ctx, ZapretMacRepo, tag)
	if err != nil {
		return Asset{}, err
	}
	return findAsset(info, func(a Asset) bool {
		return strings.Contains(strings.ToLower(a.Name), "universal") && strings.HasSuffix(a.Name, ".zip")
	})
}

// repoTags — общая выборка «релизы, где есть подходящий ассет».
func repoTags(ctx context.Context, repo string, assetOK func(string) bool) ([]string, error) {
	url := fmt.Sprintf("%s/%s/releases?per_page=15", githubAPI, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}
	var payload []struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	var tags []string
	for _, rel := range payload {
		for _, a := range rel.Assets {
			if assetOK(a.Name) {
				tags = append(tags, rel.TagName)
				break
			}
		}
	}
	return tags, nil
}

// fetchTag — информация о конкретном релизе (по тегу).
func fetchTag(ctx context.Context, repo, tag string) (*Info, error) {
	url := fmt.Sprintf("%s/%s/releases/tags/%s", githubAPI, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("релиз %s: %s", tag, resp.Status)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	info := &Info{Tag: payload.TagName}
	for _, a := range payload.Assets {
		info.Assets = append(info.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return info, nil
}

// LatestTgws — последний релиз tg-ws-proxy (Windows exe).
func LatestTgws(ctx context.Context) (tag string, asset Asset, err error) {
	info, err := fetchLatest(ctx, TgwsRepo)
	if err != nil {
		return "", Asset{}, err
	}
	asset, err = findAsset(info, func(a Asset) bool { return a.Name == "TgWsProxy_windows.exe" })
	return info.Tag, asset, err
}

// LatestTgwsMac — последний релиз tg-ws-proxy для macOS (universal dmg).
func LatestTgwsMac(ctx context.Context) (tag string, asset Asset, err error) {
	info, err := fetchLatest(ctx, TgwsRepo)
	if err != nil {
		return "", Asset{}, err
	}
	asset, err = findAsset(info, func(a Asset) bool { return a.Name == "TgWsProxy_macos_universal.dmg" })
	return info.Tag, asset, err
}

// LatestSpoofDPI — последний релиз SpoofDPI для указанной платформы
// (goos=darwin, goarch=arm64|x86_64; ассет вида spoofdpi_X_darwin_arm64.tar.gz).
func LatestSpoofDPI(ctx context.Context, goos, goarch string) (tag string, asset Asset, err error) {
	info, err := fetchLatest(ctx, SpoofDPIRepo)
	if err != nil {
		return "", Asset{}, err
	}
	suffix := fmt.Sprintf("_%s_%s.tar.gz", goos, goarch)
	asset, err = findAsset(info, func(a Asset) bool { return strings.HasSuffix(a.Name, suffix) })
	return info.Tag, asset, err
}

// SpoofDPIVersion запускает бинарь с --version (кэшируется не здесь).
// Определение версии сделано в engine-менеджере, здесь — только ассеты.

// SpoofDPIVersion — список тегов релизов spoofdpi (новейшие первыми).
func SpoofDPIVersions(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("%s/%s/releases?per_page=15", githubAPI, SpoofDPIRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api: %s", resp.Status)
	}
	var payload []struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	suffix := "_" + "darwin"
	var tags []string
	for _, rel := range payload {
		for _, a := range rel.Assets {
			if strings.Contains(a.Name, suffix) && strings.HasSuffix(a.Name, ".tar.gz") {
				tags = append(tags, rel.TagName)
				break
			}
		}
	}
	return tags, nil
}

// SpoofDPIReleaseAsset — tar.gz-ассет конкретного тега под платформу.
func SpoofDPIReleaseAsset(ctx context.Context, tag, goos, goarch string) (Asset, error) {
	url := fmt.Sprintf("%s/%s/releases/tags/%s", githubAPI, SpoofDPIRepo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Asset{}, err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Asset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Asset{}, fmt.Errorf("релиз %s: %s", tag, resp.Status)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Asset{}, err
	}
	info := &Info{Tag: payload.TagName}
	for _, a := range payload.Assets {
		info.Assets = append(info.Assets, Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	suffix := fmt.Sprintf("_%s_%s.tar.gz", goos, goarch)
	return findAsset(info, func(a Asset) bool { return strings.HasSuffix(a.Name, suffix) })
}

// Download скачивает url в dst, вызывая progress(записано, всего).
func Download(ctx context.Context, url, dst string, progress func(sent, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ZapretGui")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("скачивание: %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 64*1024)
	var lastReport time.Time
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(tmp)
				return werr
			}
			written += int64(n)
			// троттлим события прогресса до ~10 в секунду
			if time.Since(lastReport) > 100*time.Millisecond {
				lastReport = time.Now()
				if progress != nil {
					progress(written, total)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if progress != nil {
		progress(written, total)
	}
	return os.Rename(tmp, dst)
}

// Unzip распаковывает zip-архив в dest. Если весь контент лежит в одной
// корневой папке — она "срезается" (содержимое кладётся прямо в dest).
func Unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	// Определяем общую корневую папку (например "zapret-discord-youtube-1.10.1/")
	root := ""
	first := true
	for _, f := range r.File {
		name := filepath.ToSlash(f.Name)
		if i := strings.IndexByte(name, '/'); i >= 0 {
			prefix := name[:i+1]
			if first {
				root = prefix
				first = false
			} else if prefix != root {
				root = "" // нет единой корневой папки
				break
			}
		} else {
			root = ""
			break
		}
	}

	for _, f := range r.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), root)
		if name == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// Untar распаковывает tar.gz в dest (для darwin-ассетов SpoofDPI).
// Права файлов (исполняемый бит) сохраняются из заголовков tar.
func Untar(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." || strings.HasPrefix(name, "..") {
			continue // защита от path traversal
		}
		target := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, hdr.FileInfo().Mode())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

// ReadVersionFile читает версию zapret из .service/version.txt внутри дистрибутива.
func ReadVersionFile(distDir string) string {
	data, err := os.ReadFile(filepath.Join(distDir, ".service", "version.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
