package core

import (
	"context"
	_ "embed"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version — версия приложения (одна для ПК и Android).
const Version = "1.0.65"

// Profile — подписка в формате приложения (subserver.py отдаёт её по User-Agent "VPNApp/...").
type Profile struct {
	V       int    `json:"v"`
	Name    string `json:"name"`
	Tariff  string `json:"tariff"`
	Country string `json:"country"`

	Server    string `json:"server"`
	Port      int    `json:"port"`
	SNI       string `json:"sni"`
	PublicKey string `json:"pbk"`
	ShortID   string `json:"sid"`
	UUID      string `json:"uuid"`
	XHTTPPath string `json:"xhttp_path"`
	Hy2       struct {
		Obfs      string `json:"obfs"`
		Hop       string `json:"hop"`
		SNI       string `json:"sni"`
		PinSHA256 string `json:"pin_sha256"`
	} `json:"hy2"`

	DirectDomains  []string `json:"direct_domains"`
	BlockedDomains []string `json:"blocked_domains"` // заблокированные сайты в зоне .ru — через VPN
	Services      []Service `json:"services"`

	Files map[string]FileRef `json:"files"` // geo, zapret_win, app_windows, app_android

	SubURL string `json:"sub_url,omitempty"` // сохраняем локально
}

// Service — заблокированный/замедленный в РФ сервис, который работает с российским IP:
// идёт напрямую (mode=direct) или через обход DPI (mode=bypass), а если так не работает — через VPN.
type Service struct {
	ID      string   `json:"id"`
	Mode    string   `json:"mode"`
	Domains []string `json:"domains"`
	Probes  []Probe  `json:"probes"`
	// QUIC: "" — не пускать (приложение сразу уходит на TCP с обходом DPI), "direct" — напрямую
	// (если провайдер не режет QUIC сервиса — так быстрее: без перестановки пакетов на каждое соединение)
	QUIC string `json:"quic,omitempty"`
}

type Probe struct {
	URL      string `json:"url"`
	MinBytes int    `json:"min_bytes"`
}

type FileRef struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

// Встроенные корни Let's Encrypt: на старых Android их может не быть в системе,
// а сертификат сервера подписок выпущен Let's Encrypt (цепочка до ISRG Root X1).
//go:embed isrg.pem
var isrgRoots string

// DirectDialer — сокеты мимо туннеля (на ПК привязываются к реальному адаптеру, на Android процесс исключён из VPN).
var DirectDialer = &net.Dialer{Timeout: 10 * time.Second}

// Резолвер для прямых соединений самого приложения: на Android у Go нет /etc/resolv.conf.
var directResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
	var lastErr error
	for _, s := range []string{"77.88.8.8:53", "8.8.8.8:53", "1.1.1.1:53"} {
		c, err := DirectDialer.DialContext(ctx, network, s)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}}

// HTTPClient — клиент для подписки и загрузок, идёт мимо туннеля.
func HTTPClient(timeout time.Duration) *http.Client {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM([]byte(isrgRoots))
	d := *DirectDialer
	d.Resolver = directResolver
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext:         d.DialContext,
		TLSClientConfig:     &tls.Config{RootCAs: pool},
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		Proxy:               nil,
	}}
}

// UserError — понятный пользователю текст + техническая причина (для журнала).
type UserError struct {
	Msg string
	Err error
}

func (e *UserError) Error() string { return e.Msg }
func (e *UserError) Unwrap() error { return e.Err }

// Detail — полный текст ошибки для журнала.
func Detail(err error) string {
	var u *UserError
	if errors.As(err, &u) && u.Err != nil {
		return u.Msg + ": " + u.Err.Error()
	}
	return fmt.Sprint(err)
}

var ErrBadLink = errors.New("Это не похоже на ссылку подписки")

// NormalizeSubURL принимает ссылку из буфера: пробелы, кавычки, happ://add/… и т.п.
func NormalizeSubURL(s string) (string, error) {
	s = strings.Trim(strings.TrimSpace(s), "\"'<>")
	if i := strings.Index(s, "https://"); i > 0 {
		s = s[i:]
	} else if i := strings.Index(s, "http://"); i > 0 {
		s = s[i:]
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || !strings.Contains(u.Path, "/sub/") {
		return "", ErrBadLink
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// FetchProfile скачивает подписку в формате приложения.
func FetchProfile(ctx context.Context, subURL, platform string) (*Profile, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", subURL, nil)
	req.Header.Set("User-Agent", "VPNApp/"+Version+" ("+platform+")")
	req.Header.Set("Accept", "application/json")
	resp, err := HTTPClient(20 * time.Second).Do(req)
	if err != nil {
		return nil, &UserError{"Нет связи с сервером подписки", err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 || resp.StatusCode == 403 {
		return nil, errors.New("Подписка не найдена")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Сервер подписки ответил ошибкой %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, errors.New("Нет связи с сервером подписки")
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil || p.V < 1 || p.Server == "" || p.UUID == "" {
		return nil, errors.New("Эта подписка не для этого приложения")
	}
	p.SubURL = subURL
	return &p, nil
}

func LoadProfile(dir string) (*Profile, error) {
	b, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func SaveProfile(dir string, p *Profile) error {
	b, _ := json.MarshalIndent(p, "", " ")
	return writeFileAtomic(filepath.Join(dir, "profile.json"), b)
}

func writeFileAtomic(path string, b []byte) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// EnsureFile скачивает файл из подписки (geo-базы, zapret, обновления), если его sha256 не совпадает.
func EnsureFile(ctx context.Context, ref FileRef, path string) (changed bool, err error) {
	if ref.URL == "" {
		return false, errors.New("no url")
	}
	if b, err := os.ReadFile(path); err == nil && ref.SHA256 != "" && sha(b) == ref.SHA256 {
		return false, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", ref.URL, nil)
	req.Header.Set("User-Agent", "VPNApp/"+Version)
	resp, err := HTTPClient(5 * time.Minute).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return false, err
	}
	if ref.SHA256 != "" && sha(b) != ref.SHA256 {
		return false, errors.New("sha256 mismatch")
	}
	return true, writeFileAtomic(path, b)
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// SHA — sha256 в hex.
func SHA(b []byte) string { return sha(b) }
