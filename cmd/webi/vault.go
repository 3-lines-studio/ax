package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// The vault is one encrypted JSON file: scopes of `NAME -> value`, where a scope
// is usually a channel and `global` is shared by all of them. The PIN derives
// the key with scrypt; the key lives in memory until the vault is locked.
//
// A bot never reads the file: webi decrypts and hands the values to the ax
// process as environment variables, which is the only interface a provider has.

const vaultVersion = 1

// scrypt work factors. 32768 keeps an unlock near 100 ms on a laptop, which is
// slow enough to make a stolen file expensive and fast enough for a reload.
const (
	vaultScryptN = 32768
	vaultScryptR = 8
	vaultScryptP = 1
	vaultKeyLen  = 32
)

var (
	errVaultLocked   = errors.New("the vault is locked")
	errVaultMissing  = errors.New("no vault yet; create one first")
	errWrongPIN      = errors.New("wrong PIN")
	errVaultExists   = errors.New("the vault already exists")
	errPINTooShort   = errors.New("the PIN must be at least 8 characters")
	errPINMismatch   = errors.New("the PINs do not match")
	errEmptyName     = errors.New("the variable name is required")
	errInvalidName   = errors.New("a variable name is letters, digits and underscore, not starting with a digit")
	errInvalidScope  = errors.New("a scope is lowercase letters, digits, _ and -")
	errNoSuchSecret  = errors.New("no such secret")
	vaultScopeGlobal = "global"
)

type vaultFile struct {
	Version int    `json:"version"`
	Salt    string `json:"salt"`
	Nonce   string `json:"nonce"`
	Data    string `json:"data"`
}

type vaultData struct {
	Items    map[string]map[string]string `json:"items"`
	LastUsed map[string]int64             `json:"last_used"`
}

// Vault is the in-memory side: whether a file exists, whether it is unlocked,
// and the decrypted scopes when it is.
type Vault struct {
	path string

	mu     sync.Mutex
	exists bool
	key    []byte
	data   vaultData
}

func NewVault(path string) *Vault {
	vault := &Vault{path: path}
	vault.exists = fileExists(path)
	return vault
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (v *Vault) Exists() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.exists
}

func (v *Vault) Locked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.exists && v.key == nil
}

// Status is what /api/state reports: enough for the banner, never the secrets.
func (v *Vault) Status() map[string]any {
	return map[string]any{"exists": v.Exists(), "locked": v.Locked()}
}

// Names lists the scopes and variable names, never the values.
func (v *Vault) Names() (map[string][]string, map[string]int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.exists {
		return map[string][]string{}, map[string]int64{}, nil
	}
	if v.key == nil {
		return nil, nil, errVaultLocked
	}
	names := make(map[string][]string, len(v.data.Items))
	for scope, items := range v.data.Items {
		list := make([]string, 0, len(items))
		for name := range items {
			list = append(list, name)
		}
		sort.Strings(list)
		names[scope] = list
	}
	lastUsed := make(map[string]int64, len(v.data.LastUsed))
	for key, value := range v.data.LastUsed {
		lastUsed[key] = value
	}
	return names, lastUsed, nil
}

// Create makes the first vault and leaves it unlocked.
func (v *Vault) Create(pin, confirm string) error {
	if len(pin) < 8 {
		return errPINTooShort
	}
	if pin != confirm {
		return errPINMismatch
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.exists {
		return errVaultExists
	}
	v.data = vaultData{Items: map[string]map[string]string{}, LastUsed: map[string]int64{}}
	v.exists = true
	return v.saveLocked(pin)
}

// Unlock derives the key and verifies it against the stored ciphertext.
func (v *Vault) Unlock(pin string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.exists {
		return errVaultMissing
	}
	key, err := v.loadLocked(pin)
	if err != nil {
		return err
	}
	v.key = key
	return nil
}

func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.key = nil
	v.data = vaultData{}
}

// Check reports whether a PIN opens the vault without unlocking it.
func (v *Vault) Check(pin string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.exists {
		return errVaultMissing
	}
	return v.verify(pin)
}

func (v *Vault) Reveal(scope, name, pin string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return "", errVaultLocked
	}
	if err := v.verify(pin); err != nil {
		return "", err
	}
	items, ok := v.data.Items[normalizeScope(scope)]
	if !ok {
		return "", errNoSuchSecret
	}
	value, ok := items[name]
	if !ok {
		return "", errNoSuchSecret
	}
	return value, nil
}

// Put adds or overwrites one secret, and optionally renames it.
func (v *Vault) Put(scope, name, value, old string) error {
	name = strings.TrimSpace(name)
	old = strings.TrimSpace(old)
	scope = normalizeScope(scope)
	if name == "" {
		return errEmptyName
	}
	if !validSecretName(name) {
		return errInvalidName
	}
	if !validScope(scope) {
		return errInvalidScope
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return errVaultLocked
	}
	if v.data.Items[scope] == nil {
		v.data.Items[scope] = map[string]string{}
	}
	if old != "" && old != name {
		delete(v.data.Items[scope], old)
		delete(v.data.LastUsed, scope+"/"+old)
	}
	v.data.Items[scope][name] = value
	return v.save()
}

func (v *Vault) Delete(scope, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return errVaultLocked
	}
	scope = normalizeScope(scope)
	items, ok := v.data.Items[scope]
	if !ok {
		return errNoSuchSecret
	}
	if _, ok := items[name]; !ok {
		return errNoSuchSecret
	}
	delete(items, name)
	delete(v.data.LastUsed, scope+"/"+name)
	if len(items) == 0 {
		delete(v.data.Items, scope)
	}
	return v.save()
}

// Import merges a .env file: `KEY=value`, optional `export` and quotes.
// Comments and keys the text does not mention stay untouched.
func (v *Vault) Import(scope, text string) (int, error) {
	scope = normalizeScope(scope)
	if !validScope(scope) {
		return 0, errInvalidScope
	}
	parsed := parseDotEnv(text)
	if len(parsed) == 0 {
		return 0, errors.New("no KEY=value lines found")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return 0, errVaultLocked
	}
	if v.data.Items[scope] == nil {
		v.data.Items[scope] = map[string]string{}
	}
	for name, value := range parsed {
		v.data.Items[scope][name] = value
	}
	return len(parsed), v.save()
}

// Env builds the environment for one channel: global first, then the channel's
// scope on top. A `@file:NAME` secret becomes a private temporary file and the
// variable points at it, which is how a PEM or a kubeconfig reaches a tool.
// The returned cleanup removes those files when the run ends.
func (v *Vault) Env(scope string) ([]string, func(), error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.exists || v.key == nil {
		return nil, func() {}, nil
	}
	scope = normalizeScope(scope)
	merged := map[string]string{}
	for name, value := range v.data.Items[vaultScopeGlobal] {
		merged[name] = value
	}
	if scope != vaultScopeGlobal {
		for name, value := range v.data.Items[scope] {
			merged[name] = value
		}
	}
	if len(merged) == 0 {
		return nil, func() {}, nil
	}
	env := make([]string, 0, len(merged))
	var dir string
	cleanup := func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	now := time.Now().Unix()
	for name, value := range merged {
		if strings.HasPrefix(name, "@file:") {
			variable := strings.TrimPrefix(name, "@file:")
			if !validSecretName(variable) {
				continue
			}
			if dir == "" {
				created, err := os.MkdirTemp("", "webi-secret-")
				if err != nil {
					cleanup()
					return nil, func() {}, err
				}
				dir = created
			}
			path := filepath.Join(dir, variable)
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				cleanup()
				return nil, func() {}, err
			}
			env = append(env, variable+"="+path)
			v.data.LastUsed[scope+"/"+name] = now
			continue
		}
		if !validSecretName(name) {
			continue
		}
		env = append(env, name+"="+value)
		v.data.LastUsed[scope+"/"+name] = now
	}
	sort.Strings(env)
	_ = v.save()
	return env, cleanup, nil
}

// Has reports whether a variable exists in any scope the channel sees.
func (v *Vault) Has(scope, name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return false
	}
	scope = normalizeScope(scope)
	if _, ok := v.data.Items[scope][name]; ok {
		return true
	}
	_, ok := v.data.Items[vaultScopeGlobal][name]
	return ok
}

// --- storage ---------------------------------------------------------------

func (v *Vault) verify(pin string) error {
	file, err := readVaultFile(v.path)
	if err != nil {
		return err
	}
	salt, err := base64.StdEncoding.DecodeString(file.Salt)
	if err != nil {
		return err
	}
	if _, err := decrypt(file, vaultKey(pin, salt)); err != nil {
		return errWrongPIN
	}
	return nil
}

// Key returns a copy of the derived key, or nil while the vault is locked.
func (v *Vault) Key() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]byte(nil), v.key...)
}

// Adopt installs a key the host kept across restarts. The key is verified
// against the file, so a stale one leaves the vault locked instead of
// corrupting it on the next write.
func (v *Vault) Adopt(key []byte) error {
	if len(key) != vaultKeyLen {
		return errWrongPIN
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.exists {
		return nil
	}
	file, err := readVaultFile(v.path)
	if err != nil {
		return err
	}
	plain, err := decrypt(file, key)
	if err != nil {
		return errWrongPIN
	}
	var data vaultData
	if err := json.Unmarshal(plain, &data); err != nil {
		return err
	}
	if data.Items == nil {
		data.Items = map[string]map[string]string{}
	}
	if data.LastUsed == nil {
		data.LastUsed = map[string]int64{}
	}
	v.key = append([]byte(nil), key...)
	v.data = data
	return nil
}

// loadLocked decrypts the file into memory and returns the key, so Unlock does
// not have to derive it twice.
func (v *Vault) loadLocked(pin string) ([]byte, error) {
	file, err := readVaultFile(v.path)
	if err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(file.Salt)
	if err != nil {
		return nil, err
	}
	key := vaultKey(pin, salt)
	plain, err := decrypt(file, key)
	if err != nil {
		return nil, errWrongPIN
	}
	var data vaultData
	if err := json.Unmarshal(plain, &data); err != nil {
		return nil, err
	}
	if data.Items == nil {
		data.Items = map[string]map[string]string{}
	}
	if data.LastUsed == nil {
		data.LastUsed = map[string]int64{}
	}
	v.data = data
	return key, nil
}

func (v *Vault) saveLocked(pin string) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key := vaultKey(pin, salt)
	file, err := encrypt(v.data, salt, key)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(v.path, file, 0o600); err != nil {
		return err
	}
	v.key = key
	return nil
}

func (v *Vault) save() error {
	file, err := readVaultFile(v.path)
	if err != nil {
		return err
	}
	salt, err := base64.StdEncoding.DecodeString(file.Salt)
	if err != nil {
		return err
	}
	encoded, err := encrypt(v.data, salt, v.key)
	if err != nil {
		return err
	}
	return writeFileAtomic(v.path, encoded, 0o600)
}

func readVaultFile(path string) (vaultFile, error) {
	var file vaultFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, errVaultMissing
		}
		return file, err
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("vault file: %w", err)
	}
	if file.Version != vaultVersion {
		return file, fmt.Errorf("vault version %d is not supported", file.Version)
	}
	return file, nil
}

func vaultKey(pin string, salt []byte) []byte {
	key, err := scrypt.Key([]byte(pin), salt, vaultScryptN, vaultScryptR, vaultScryptP, vaultKeyLen)
	if err != nil {
		// scrypt only fails on impossible parameters, which are constants here.
		panic(err)
	}
	return key
}

func encrypt(data vaultData, salt, key []byte) ([]byte, error) {
	plain, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	file := vaultFile{
		Version: vaultVersion,
		Salt:    base64.StdEncoding.EncodeToString(salt),
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		Data:    base64.StdEncoding.EncodeToString(sealed),
	}
	return json.MarshalIndent(file, "", "  ")
}

func decrypt(file vaultFile, key []byte) ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(file.Nonce)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(file.Data)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errWrongPIN
	}
	return gcm.Open(nil, nonce, sealed, nil)
}

// --- names -----------------------------------------------------------------

// normalizeScope turns an empty or channel-like value into a usable scope.
func normalizeScope(scope string) string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope == "" {
		return vaultScopeGlobal
	}
	return scope
}

func validScope(scope string) bool {
	if scope == "" || len(scope) > 64 {
		return false
	}
	for _, r := range scope {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validSecretName accepts both plain variables and the @file: prefix, which is
// how a multi-line secret reaches a tool as a path.
func validSecretName(name string) bool {
	name = strings.TrimPrefix(name, "@file:")
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// parseDotEnv reads the subset of .env people actually paste: comments, blank
// lines, export, single/double quotes and inline values.
func parseDotEnv(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !validSecretName(name) {
			continue
		}
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		out[name] = value
	}
	return out
}
