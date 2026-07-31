package secrets

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/ardasevinc/tele/internal/privatefs"
)

const (
	vaultFormatVersion   = uint16(1)
	vaultPayloadSchema   = uint16(1)
	vaultKDFArgon2id     = byte(1)
	vaultCipherXChaCha   = byte(1)
	vaultHeaderSize      = 168
	vaultWrapAADSize     = 82
	vaultWrappedKeyEnd   = 130
	vaultMaxSize         = 4 << 20
	vaultMaxRecords      = 4096
	vaultMaxKeySize      = 256
	vaultMaxValueSize    = 1 << 20
	vaultArgonMemory     = uint32(64 * 1024)
	vaultArgonTime       = uint32(3)
	vaultArgonThreads    = byte(1)
	vaultMaxArgonMemory  = uint32(1024 * 1024)
	vaultMaxArgonTime    = uint32(10)
	vaultMaxArgonThreads = byte(16)
)

var (
	vaultMagic                 = [8]byte{'T', 'E', 'L', 'E', 'V', 'L', 'T', 0}
	ErrVaultUnlockFailed       = errors.New("vault unlock failed")
	ErrVaultCorrupt            = errors.New("vault corrupt")
	ErrVaultVersionUnsupported = errors.New("vault version unsupported")
)

type VaultError struct {
	Kind   error
	Detail string
}

func (e *VaultError) Error() string {
	if e == nil || e.Kind == nil {
		return "vault error"
	}
	if e.Detail == "" {
		return e.Kind.Error()
	}
	return e.Kind.Error() + ": " + e.Detail
}

func (e *VaultError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

type VaultStore struct {
	path       string
	profile    string
	instance   string
	instanceID [16]byte
	masterKey  [32]byte
}

type vaultPayload struct {
	Schema     uint16            `json:"schema"`
	VaultUUID  string            `json:"vault_uuid"`
	Profile    string            `json:"profile"`
	Generation uint64            `json:"generation"`
	Records    map[string][]byte `json:"records"`
}

type vaultHeader struct {
	instanceID [16]byte
	memory     uint32
	time       uint32
	threads    byte
	schema     uint16
	generation uint64
	payloadLen uint32
}

func CreateVault(ctx context.Context, path, profile, instance string, passphrase []byte) (*VaultStore, error) {
	if err := validateVaultIdentity(profile, instance); err != nil {
		return nil, err
	}
	if len(passphrase) == 0 {
		return nil, fmt.Errorf("vault passphrase must not be empty")
	}
	if err := rejectExistingSymlinkComponents(path); err != nil {
		return nil, err
	}
	instanceID, err := parseUUID(instance)
	if err != nil {
		return nil, err
	}
	store := &VaultStore{path: path, profile: profile, instance: instance, instanceID: instanceID}
	if _, err := rand.Read(store.masterKey[:]); err != nil {
		return nil, fmt.Errorf("generate vault master key: %w", err)
	}
	created := false
	profileLock := filepath.Join(filepath.Dir(path), "profile.lock")
	err = withProfileLockPath(ctx, profileLock, func(ctx context.Context) error {
		return privatefs.WithLock(ctx, path+".lock", func() error {
			if _, err := os.Lstat(path); err == nil {
				return fmt.Errorf("vault already exists: %s", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			encoded, err := createVaultBytes(profile, instance, instanceID, passphrase, store.masterKey[:])
			if err != nil {
				return err
			}
			defer zeroBytes(encoded)
			if err := privatefs.AtomicWriteFile(path, encoded); err != nil {
				return err
			}
			created = true
			return nil
		})
	})
	if err != nil {
		zeroBytes(store.masterKey[:])
		return nil, err
	}
	if !created {
		zeroBytes(store.masterKey[:])
		return nil, fmt.Errorf("vault was not created")
	}
	return store, nil
}

func OpenVault(path, profile, instance string, passphrase []byte) (*VaultStore, error) {
	if err := validateVaultIdentity(profile, instance); err != nil {
		return nil, err
	}
	if len(passphrase) == 0 {
		return nil, &VaultError{Kind: ErrVaultUnlockFailed}
	}
	instanceID, err := parseUUID(instance)
	if err != nil {
		return nil, err
	}
	data, err := readVaultFile(path)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(data)
	header, err := parseVaultHeader(data, instanceID)
	if err != nil {
		return nil, err
	}
	wrappingKey := argon2.IDKey(passphrase, data[30:46], header.time, header.memory, header.threads, chacha20poly1305.KeySize)
	defer zeroBytes(wrappingKey)
	wrapCipher, err := chacha20poly1305.NewX(wrappingKey)
	if err != nil {
		return nil, err
	}
	masterKey, err := wrapCipher.Open(nil, data[58:82], data[82:130], data[:vaultWrapAADSize])
	if err != nil || len(masterKey) != chacha20poly1305.KeySize {
		zeroBytes(masterKey)
		return nil, &VaultError{Kind: ErrVaultUnlockFailed}
	}
	defer zeroBytes(masterKey)
	if _, err := decryptVaultPayload(data, header, profile, instance, masterKey); err != nil {
		return nil, err
	}
	store := &VaultStore{path: path, profile: profile, instance: instance, instanceID: instanceID}
	copy(store.masterKey[:], masterKey)
	return store, nil
}

func (s *VaultStore) Close() {
	if s != nil {
		zeroBytes(s.masterKey[:])
	}
}

func (s *VaultStore) BackendInfo() BackendInfo {
	if s == nil {
		return BackendInfo{ID: BackendVault, Name: "portable vault", Supported: true}
	}
	return BackendInfo{ID: BackendVault, Instance: s.instance, Name: "portable vault", Supported: true}
}

func (s *VaultStore) Get(ctx context.Context, profile, key string) ([]byte, error) {
	if err := s.validateAccess(profile, key, nil); err != nil {
		return nil, err
	}
	var value []byte
	err := s.withLock(ctx, func(context.Context) error {
		data, payload, err := s.readCurrent()
		if err != nil {
			return err
		}
		defer zeroBytes(data)
		stored, ok := payload.Records[key]
		if !ok {
			return ErrNotFound
		}
		value = append([]byte(nil), stored...)
		return nil
	})
	return value, err
}

func (s *VaultStore) Set(ctx context.Context, profile, key string, value []byte) error {
	if err := s.validateAccess(profile, key, value); err != nil {
		return err
	}
	return s.withLock(ctx, func(context.Context) error {
		data, payload, err := s.readCurrent()
		if err != nil {
			return err
		}
		defer zeroBytes(data)
		if _, exists := payload.Records[key]; !exists && len(payload.Records) >= vaultMaxRecords {
			return fmt.Errorf("vault record limit exceeded")
		}
		payload.Records[key] = append([]byte(nil), value...)
		return s.writePayload(data, payload)
	})
}

func (s *VaultStore) Delete(ctx context.Context, profile, key string) error {
	if err := s.validateAccess(profile, key, nil); err != nil {
		return err
	}
	return s.withLock(ctx, func(context.Context) error {
		data, payload, err := s.readCurrent()
		if err != nil {
			return err
		}
		defer zeroBytes(data)
		if _, exists := payload.Records[key]; !exists {
			return nil
		}
		delete(payload.Records, key)
		return s.writePayload(data, payload)
	})
}

func (s *VaultStore) Snapshot(ctx context.Context) (map[string][]byte, error) {
	var snapshot map[string][]byte
	err := s.withLock(ctx, func(context.Context) error {
		data, payload, err := s.readCurrent()
		if err != nil {
			return err
		}
		defer zeroBytes(data)
		snapshot = cloneRecords(payload.Records)
		return nil
	})
	return snapshot, err
}

func (s *VaultStore) VaultDiagnostics(ctx context.Context) (VaultDiagnostics, error) {
	var diagnostics VaultDiagnostics
	err := s.withLock(ctx, func(context.Context) error {
		data, payload, err := s.readCurrent()
		if err != nil {
			return err
		}
		defer zeroBytes(data)
		header, err := parseVaultHeader(data, s.instanceID)
		if err != nil {
			return err
		}
		diagnostics = VaultDiagnostics{
			Path: s.path, FormatVersion: vaultFormatVersion, PayloadSchema: header.schema,
			Generation: header.generation, ArgonMemoryKiB: header.memory,
			ArgonIterations: header.time, ArgonParallelism: header.threads,
			Records: len(payload.Records),
		}
		return nil
	})
	return diagnostics, err
}

func (s *VaultStore) validateAccess(profile, key string, value []byte) error {
	if s == nil {
		return fmt.Errorf("vault store is nil")
	}
	if profile != s.profile {
		return fmt.Errorf("vault profile mismatch: got %q, want %q", profile, s.profile)
	}
	if !utf8.ValidString(key) || len(key) == 0 || len(key) > vaultMaxKeySize {
		return fmt.Errorf("vault key must be valid UTF-8 and 1..%d bytes", vaultMaxKeySize)
	}
	if len(value) > vaultMaxValueSize {
		return fmt.Errorf("vault value exceeds %d bytes", vaultMaxValueSize)
	}
	return nil
}

func (s *VaultStore) withLock(ctx context.Context, fn func(context.Context) error) error {
	profileLock := filepath.Join(filepath.Dir(s.path), "profile.lock")
	return withProfileLockPath(ctx, profileLock, func(ctx context.Context) error {
		return privatefs.WithLock(ctx, s.path+".lock", func() error { return fn(ctx) })
	})
}

func (s *VaultStore) readCurrent() ([]byte, vaultPayload, error) {
	data, err := readVaultFile(s.path)
	if err != nil {
		return nil, vaultPayload{}, err
	}
	header, err := parseVaultHeader(data, s.instanceID)
	if err != nil {
		zeroBytes(data)
		return nil, vaultPayload{}, err
	}
	payload, err := decryptVaultPayload(data, header, s.profile, s.instance, s.masterKey[:])
	if err != nil {
		zeroBytes(data)
		return nil, vaultPayload{}, err
	}
	return data, payload, nil
}

func (s *VaultStore) writePayload(current []byte, payload vaultPayload) error {
	if payload.Generation == math.MaxUint64 {
		return &VaultError{Kind: ErrVaultCorrupt, Detail: "generation overflow"}
	}
	payload.Generation++
	encoded, err := encodeVaultPayload(current[:vaultWrappedKeyEnd], payload, s.masterKey[:])
	if err != nil {
		return err
	}
	defer zeroBytes(encoded)
	return privatefs.AtomicWriteFile(s.path, encoded)
}

func createVaultBytes(profile, instance string, instanceID [16]byte, passphrase, masterKey []byte) ([]byte, error) {
	prefix := make([]byte, vaultWrappedKeyEnd)
	copy(prefix[:8], vaultMagic[:])
	binary.BigEndian.PutUint16(prefix[8:10], vaultFormatVersion)
	prefix[10] = vaultKDFArgon2id
	prefix[11] = vaultCipherXChaCha
	prefix[12] = vaultCipherXChaCha
	copy(prefix[14:30], instanceID[:])
	if _, err := rand.Read(prefix[30:46]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(prefix[46:50], vaultArgonMemory)
	binary.BigEndian.PutUint32(prefix[50:54], vaultArgonTime)
	prefix[54] = vaultArgonThreads
	if _, err := rand.Read(prefix[58:82]); err != nil {
		return nil, err
	}
	wrappingKey := argon2.IDKey(passphrase, prefix[30:46], vaultArgonTime, vaultArgonMemory, vaultArgonThreads, chacha20poly1305.KeySize)
	defer zeroBytes(wrappingKey)
	wrapCipher, err := chacha20poly1305.NewX(wrappingKey)
	if err != nil {
		return nil, err
	}
	wrapped := wrapCipher.Seal(nil, prefix[58:82], masterKey, prefix[:vaultWrapAADSize])
	if len(wrapped) != 48 {
		return nil, fmt.Errorf("unexpected wrapped key length %d", len(wrapped))
	}
	copy(prefix[82:130], wrapped)
	zeroBytes(wrapped)
	return encodeVaultPayload(prefix, vaultPayload{
		Schema: vaultPayloadSchema, VaultUUID: instance, Profile: profile,
		Generation: 1, Records: map[string][]byte{},
	}, masterKey)
}

func encodeVaultPayload(prefix []byte, payload vaultPayload, masterKey []byte) ([]byte, error) {
	if len(prefix) != vaultWrappedKeyEnd {
		return nil, fmt.Errorf("invalid vault prefix length")
	}
	if err := validatePayload(payload, payload.Profile, payload.VaultUUID, payload.Generation); err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(plaintext)
	if len(plaintext) > vaultMaxSize {
		return nil, fmt.Errorf("vault payload exceeds %d bytes", vaultMaxSize)
	}
	dataKey, err := hkdf.Key(sha256.New, masterKey, nil, "tele vault data v1", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(dataKey)
	payloadCipher, err := chacha20poly1305.NewX(dataKey)
	if err != nil {
		return nil, err
	}
	ciphertextLen := len(plaintext) + payloadCipher.Overhead()
	if vaultHeaderSize+ciphertextLen > vaultMaxSize {
		return nil, fmt.Errorf("vault file exceeds %d bytes", vaultMaxSize)
	}
	encoded := make([]byte, vaultHeaderSize, vaultHeaderSize+ciphertextLen)
	copy(encoded[:vaultWrappedKeyEnd], prefix)
	binary.BigEndian.PutUint16(encoded[130:132], payload.Schema)
	binary.BigEndian.PutUint64(encoded[132:140], payload.Generation)
	if _, err := rand.Read(encoded[140:164]); err != nil {
		return nil, err
	}
	// #nosec G115 -- ciphertextLen is bounded above by vaultMaxSize immediately above.
	binary.BigEndian.PutUint32(encoded[164:168], uint32(ciphertextLen))
	aad := payloadAAD(encoded)
	encoded = payloadCipher.Seal(encoded, encoded[140:164], plaintext, aad)
	return encoded, nil
}

func parseVaultHeader(data []byte, expectedID [16]byte) (vaultHeader, error) {
	corrupt := func(detail string) (vaultHeader, error) {
		return vaultHeader{}, &VaultError{Kind: ErrVaultCorrupt, Detail: detail}
	}
	if len(data) < vaultHeaderSize || len(data) > vaultMaxSize {
		return corrupt("invalid file size")
	}
	if !bytes.Equal(data[:8], vaultMagic[:]) {
		return corrupt("invalid magic")
	}
	version := binary.BigEndian.Uint16(data[8:10])
	if version != vaultFormatVersion {
		return vaultHeader{}, &VaultError{Kind: ErrVaultVersionUnsupported, Detail: fmt.Sprintf("format %d", version)}
	}
	if data[10] != vaultKDFArgon2id || data[11] != vaultCipherXChaCha || data[12] != vaultCipherXChaCha {
		return corrupt("unknown cryptographic identifier")
	}
	if data[13] != 0 || data[55] != 0 || data[56] != 0 || data[57] != 0 {
		return corrupt("reserved bytes are nonzero")
	}
	var instanceID [16]byte
	copy(instanceID[:], data[14:30])
	if instanceID != expectedID {
		return corrupt("vault instance does not match selector")
	}
	memory := binary.BigEndian.Uint32(data[46:50])
	timeCost := binary.BigEndian.Uint32(data[50:54])
	threads := data[54]
	if memory == 0 || memory > vaultMaxArgonMemory || timeCost == 0 || timeCost > vaultMaxArgonTime || threads == 0 || threads > vaultMaxArgonThreads {
		return corrupt("invalid Argon2 parameters")
	}
	schema := binary.BigEndian.Uint16(data[130:132])
	if schema != vaultPayloadSchema {
		return corrupt("unsupported payload schema")
	}
	generation := binary.BigEndian.Uint64(data[132:140])
	if generation == 0 {
		return corrupt("zero generation")
	}
	payloadLen := binary.BigEndian.Uint32(data[164:168])
	if payloadLen < chacha20poly1305.Overhead || payloadLen > vaultMaxSize-vaultHeaderSize || int(payloadLen) != len(data)-vaultHeaderSize {
		return corrupt("invalid payload length")
	}
	return vaultHeader{instanceID: instanceID, memory: memory, time: timeCost, threads: threads, schema: schema, generation: generation, payloadLen: payloadLen}, nil
}

func decryptVaultPayload(data []byte, header vaultHeader, profile, instance string, masterKey []byte) (vaultPayload, error) {
	dataKey, err := hkdf.Key(sha256.New, masterKey, nil, "tele vault data v1", chacha20poly1305.KeySize)
	if err != nil {
		return vaultPayload{}, err
	}
	defer zeroBytes(dataKey)
	payloadCipher, err := chacha20poly1305.NewX(dataKey)
	if err != nil {
		return vaultPayload{}, err
	}
	plaintext, err := payloadCipher.Open(nil, data[140:164], data[vaultHeaderSize:], payloadAAD(data))
	if err != nil {
		return vaultPayload{}, &VaultError{Kind: ErrVaultUnlockFailed}
	}
	defer zeroBytes(plaintext)
	if len(plaintext) > vaultMaxSize {
		return vaultPayload{}, &VaultError{Kind: ErrVaultCorrupt, Detail: "payload too large"}
	}
	if err := rejectDuplicateJSONKeys(plaintext); err != nil {
		return vaultPayload{}, &VaultError{Kind: ErrVaultCorrupt, Detail: err.Error()}
	}
	var payload vaultPayload
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return vaultPayload{}, &VaultError{Kind: ErrVaultCorrupt, Detail: "invalid payload"}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return vaultPayload{}, &VaultError{Kind: ErrVaultCorrupt, Detail: "trailing payload data"}
	}
	if err := validatePayload(payload, profile, instance, header.generation); err != nil {
		return vaultPayload{}, &VaultError{Kind: ErrVaultCorrupt, Detail: err.Error()}
	}
	return payload, nil
}

func payloadAAD(data []byte) []byte {
	aad := make([]byte, 0, 65)
	aad = append(aad, data[:10]...)
	aad = append(aad, data[14:30]...)
	aad = append(aad, data[12])
	aad = append(aad, data[130:168]...)
	return aad
}

func validatePayload(payload vaultPayload, profile, instance string, generation uint64) error {
	if payload.Schema != vaultPayloadSchema || payload.VaultUUID != instance || payload.Profile != profile || payload.Generation != generation {
		return fmt.Errorf("payload identity mismatch")
	}
	if !utf8.ValidString(profile) || len(profile) == 0 || len(profile) > vaultMaxKeySize {
		return fmt.Errorf("invalid payload profile")
	}
	if payload.Records == nil {
		return fmt.Errorf("records must not be null")
	}
	if len(payload.Records) > vaultMaxRecords {
		return fmt.Errorf("too many records")
	}
	for key, value := range payload.Records {
		if !utf8.ValidString(key) || len(key) == 0 || len(key) > vaultMaxKeySize {
			return fmt.Errorf("invalid record key")
		}
		if len(value) > vaultMaxValueSize {
			return fmt.Errorf("record value too large")
		}
	}
	return nil
}

func validateVaultIdentity(profile, instance string) error {
	if !utf8.ValidString(profile) || len(profile) == 0 || len(profile) > vaultMaxKeySize {
		return fmt.Errorf("vault profile must be valid UTF-8 and 1..%d bytes", vaultMaxKeySize)
	}
	if _, err := parseUUID(instance); err != nil {
		return err
	}
	return nil
}

func readVaultFile(path string) ([]byte, error) {
	if err := rejectExistingSymlinkComponents(path); err != nil {
		return nil, err
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() || dirInfo.Mode().Perm()&0o077 != 0 || !vaultOwnerAllowed(dirInfo) {
		return nil, &VaultError{Kind: ErrVaultCorrupt, Detail: "unsafe vault directory"}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !vaultOwnerAllowed(info) {
		return nil, &VaultError{Kind: ErrVaultCorrupt, Detail: "vault is not a regular file"}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, &VaultError{Kind: ErrVaultCorrupt, Detail: "unsafe vault permissions"}
	}
	if info.Size() < vaultHeaderSize || info.Size() > vaultMaxSize {
		return nil, &VaultError{Kind: ErrVaultCorrupt, Detail: "invalid file size"}
	}
	file, err := openVaultFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o077 != 0 || !vaultOwnerAllowed(openedInfo) || !os.SameFile(info, openedInfo) {
		return nil, &VaultError{Kind: ErrVaultCorrupt, Detail: "vault changed during secure open"}
	}
	data, err := io.ReadAll(io.LimitReader(file, vaultMaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > vaultMaxSize {
		zeroBytes(data)
		return nil, &VaultError{Kind: ErrVaultCorrupt, Detail: "vault exceeds size limit"}
	}
	return data, nil
}

func parseUUID(value string) ([16]byte, error) {
	var id [16]byte
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return id, fmt.Errorf("invalid vault instance UUID %q", value)
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != len(id) {
		return id, fmt.Errorf("invalid vault instance UUID %q", value)
	}
	copy(id[:], decoded)
	if id == [16]byte{} {
		return id, fmt.Errorf("invalid zero vault instance UUID")
	}
	return id, nil
}

func FormatUUID(id [16]byte) string {
	hexValue := hex.EncodeToString(id[:])
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:]
}

func NewVaultInstance() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return FormatUUID(id), nil
}

func VaultPath(dataRoot, profile, instance string) string {
	return filepath.Join(dataRoot, profile, "secrets", instance+".vault")
}

func ValidateVaultInstance(instance string) error {
	_, err := parseUUID(instance)
	return err
}

func InspectVaultInstance(path, instance string) error {
	instanceID, err := parseUUID(instance)
	if err != nil {
		return err
	}
	data, err := readVaultFile(path)
	if err != nil {
		return err
	}
	defer zeroBytes(data)
	_, err = parseVaultHeader(data, instanceID)
	return err
}

func PurgeVault(ctx context.Context, path, instance string) error {
	profileLock := filepath.Join(filepath.Dir(path), "profile.lock")
	return withProfileLockPath(ctx, profileLock, func(ctx context.Context) error {
		return privatefs.WithLock(ctx, path+".lock", func() error {
			if err := InspectVaultInstance(path, instance); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			dir, err := os.Open(filepath.Dir(path))
			if err != nil {
				return err
			}
			return errors.Join(dir.Sync(), dir.Close())
		})
	})
}

func rejectExistingSymlinkComponents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	separator := string(filepath.Separator)
	volume := filepath.VolumeName(absolute)
	current := volume + separator
	remainder := strings.TrimPrefix(strings.TrimPrefix(filepath.Clean(absolute), volume), separator)
	for _, component := range strings.Split(remainder, separator) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &VaultError{Kind: ErrVaultCorrupt, Detail: "vault path contains a symlink"}
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("non-string object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values")
}

func cloneRecords(records map[string][]byte) map[string][]byte {
	clone := make(map[string][]byte, len(records))
	for key, value := range records {
		clone[key] = append([]byte(nil), value...)
	}
	return clone
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
