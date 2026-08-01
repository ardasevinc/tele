//go:build darwin

package secrets

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const nativeKeychainService = "tele-v1"

type securityFrameworkKeychain struct {
	cfRelease                  func(uintptr)
	cfStringCreate             func(uintptr, string, uint32) uintptr
	cfDataCreate               func(uintptr, *byte, int64) uintptr
	cfDataGetLength            func(uintptr) int64
	cfDataGetBytePtr           func(uintptr) uintptr
	cfDataGetTypeID            func() uintptr
	cfGetTypeID                func(uintptr) uintptr
	cfDictionaryCreate         func(uintptr, *uintptr, *uintptr, int64, uintptr, uintptr) uintptr
	secItemAdd                 func(uintptr, *uintptr) int32
	secItemUpdate              func(uintptr, uintptr) int32
	secItemCopyMatching        func(uintptr, *uintptr) int32
	secItemDelete              func(uintptr) int32
	dictionaryKeyCallbacks     uintptr
	dictionaryValueCallbacks   uintptr
	secClass                   uintptr
	secClassGenericPassword    uintptr
	secAttrService             uintptr
	secAttrAccount             uintptr
	secValueData               uintptr
	secReturnData              uintptr
	secMatchLimit              uintptr
	secMatchLimitOne           uintptr
	secUseAuthenticationUI     uintptr
	secUseAuthenticationUIFail uintptr
	cfBooleanTrue              uintptr
}

var (
	nativeKeychainOnce        sync.Once
	nativeKeychainAPIInstance nativeKeychainAPI
	nativeKeychainConnectErr  error
)

func connectNativeKeychain() (nativeKeychainAPI, error) {
	nativeKeychainOnce.Do(func() {
		nativeKeychainAPIInstance, nativeKeychainConnectErr = loadSecurityFrameworkKeychain()
		if nativeKeychainConnectErr != nil {
			nativeKeychainConnectErr = &BackendError{Kind: ErrBackendUnavailable, Backend: BackendKeychain, Detail: nativeKeychainConnectErr.Error()}
		}
	})
	return nativeKeychainAPIInstance, nativeKeychainConnectErr
}

func loadSecurityFrameworkKeychain() (*securityFrameworkKeychain, error) {
	coreFoundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load CoreFoundation: %w", err)
	}
	security, err := purego.Dlopen("/System/Library/Frameworks/Security.framework/Security", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load Security.framework: %w", err)
	}
	api := &securityFrameworkKeychain{}
	bindings := []struct {
		handle uintptr
		name   string
		target any
	}{
		{coreFoundation, "CFRelease", &api.cfRelease},
		{coreFoundation, "CFStringCreateWithCString", &api.cfStringCreate},
		{coreFoundation, "CFDataCreate", &api.cfDataCreate},
		{coreFoundation, "CFDataGetLength", &api.cfDataGetLength},
		{coreFoundation, "CFDataGetBytePtr", &api.cfDataGetBytePtr},
		{coreFoundation, "CFDataGetTypeID", &api.cfDataGetTypeID},
		{coreFoundation, "CFGetTypeID", &api.cfGetTypeID},
		{coreFoundation, "CFDictionaryCreate", &api.cfDictionaryCreate},
		{security, "SecItemAdd", &api.secItemAdd},
		{security, "SecItemUpdate", &api.secItemUpdate},
		{security, "SecItemCopyMatching", &api.secItemCopyMatching},
		{security, "SecItemDelete", &api.secItemDelete},
	}
	for _, binding := range bindings {
		if err := bindFrameworkFunction(binding.handle, binding.name, binding.target); err != nil {
			return nil, err
		}
	}
	if api.dictionaryKeyCallbacks, err = purego.Dlsym(coreFoundation, "kCFTypeDictionaryKeyCallBacks"); err != nil {
		return nil, err
	}
	if api.dictionaryValueCallbacks, err = purego.Dlsym(coreFoundation, "kCFTypeDictionaryValueCallBacks"); err != nil {
		return nil, err
	}
	constants := []struct {
		handle uintptr
		name   string
		target *uintptr
	}{
		{security, "kSecClass", &api.secClass},
		{security, "kSecClassGenericPassword", &api.secClassGenericPassword},
		{security, "kSecAttrService", &api.secAttrService},
		{security, "kSecAttrAccount", &api.secAttrAccount},
		{security, "kSecValueData", &api.secValueData},
		{security, "kSecReturnData", &api.secReturnData},
		{security, "kSecMatchLimit", &api.secMatchLimit},
		{security, "kSecMatchLimitOne", &api.secMatchLimitOne},
		{security, "kSecUseAuthenticationUI", &api.secUseAuthenticationUI},
		{security, "kSecUseAuthenticationUIFail", &api.secUseAuthenticationUIFail},
		{coreFoundation, "kCFBooleanTrue", &api.cfBooleanTrue},
	}
	for _, constant := range constants {
		if *constant.target, err = loadFrameworkReference(constant.handle, constant.name); err != nil {
			return nil, err
		}
	}
	return api, nil
}

func bindFrameworkFunction(handle uintptr, name string, target any) error {
	symbol, err := purego.Dlsym(handle, name)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", name, err)
	}
	purego.RegisterFunc(target, symbol)
	return nil
}

func loadFrameworkReference(handle uintptr, name string) (uintptr, error) {
	symbol, err := purego.Dlsym(handle, name)
	if err != nil {
		return 0, fmt.Errorf("resolve %s: %w", name, err)
	}
	reference := *(*uintptr)(unsafe.Pointer(symbol))
	if reference == 0 {
		return 0, fmt.Errorf("resolve %s: nil reference", name)
	}
	return reference, nil
}

func (s *securityFrameworkKeychain) Create(ctx context.Context, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	query, release, err := s.itemDictionary(account, value, true)
	if err != nil {
		return err
	}
	defer release()
	status := s.secItemAdd(query, nil)
	if err := ctx.Err(); err != nil {
		return err
	}
	return classifySecurityStatus(status)
}

func (s *securityFrameworkKeychain) Replace(ctx context.Context, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	query, releaseQuery, err := s.itemDictionary(account, nil, false)
	if err != nil {
		return err
	}
	defer releaseQuery()
	data, err := s.newData(value)
	if err != nil {
		return err
	}
	defer s.cfRelease(data)
	update, err := s.newDictionary([]uintptr{s.secValueData}, []uintptr{data})
	if err != nil {
		return err
	}
	defer s.cfRelease(update)
	status := s.secItemUpdate(query, update)
	if err := ctx.Err(); err != nil {
		return err
	}
	return classifySecurityStatus(status)
}

func (s *securityFrameworkKeychain) Get(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	service, err := s.newString(nativeKeychainService)
	if err != nil {
		return nil, err
	}
	defer s.cfRelease(service)
	accountRef, err := s.newString(account)
	if err != nil {
		return nil, err
	}
	defer s.cfRelease(accountRef)
	query, err := s.newDictionary(
		[]uintptr{s.secClass, s.secAttrService, s.secAttrAccount, s.secReturnData, s.secMatchLimit, s.secUseAuthenticationUI},
		[]uintptr{s.secClassGenericPassword, service, accountRef, s.cfBooleanTrue, s.secMatchLimitOne, s.secUseAuthenticationUIFail},
	)
	if err != nil {
		return nil, err
	}
	defer s.cfRelease(query)
	var result uintptr
	status := s.secItemCopyMatching(query, &result)
	if err := ctx.Err(); err != nil {
		if result != 0 {
			s.cfRelease(result)
		}
		return nil, err
	}
	if err := classifySecurityStatus(status); err != nil {
		return nil, err
	}
	if result == 0 {
		return nil, fmt.Errorf("SecItemCopyMatching returned no data")
	}
	defer s.cfRelease(result)
	if s.cfGetTypeID(result) != s.cfDataGetTypeID() {
		return nil, fmt.Errorf("SecItemCopyMatching returned unexpected type")
	}
	length := s.cfDataGetLength(result)
	if length < 0 || length > vaultMaxSize {
		return nil, &BackendError{Kind: ErrCatalogIncomplete, Backend: BackendKeychain, Detail: "keychain item exceeds size limit"}
	}
	if length == 0 {
		return []byte{}, nil
	}
	pointer := s.cfDataGetBytePtr(result)
	if pointer == 0 {
		return nil, fmt.Errorf("CFDataGetBytePtr returned nil")
	}
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(pointer)), int(length))...), nil
}

func (s *securityFrameworkKeychain) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	query, release, err := s.itemDictionary(account, nil, false)
	if err != nil {
		return err
	}
	defer release()
	status := s.secItemDelete(query)
	if err := ctx.Err(); err != nil {
		return err
	}
	return classifySecurityStatus(status)
}

func (s *securityFrameworkKeychain) itemDictionary(account string, value []byte, includeValue bool) (uintptr, func(), error) {
	service, err := s.newString(nativeKeychainService)
	if err != nil {
		return 0, nil, err
	}
	accountRef, err := s.newString(account)
	if err != nil {
		s.cfRelease(service)
		return 0, nil, err
	}
	owned := []uintptr{service, accountRef}
	keys := []uintptr{s.secClass, s.secAttrService, s.secAttrAccount, s.secUseAuthenticationUI}
	values := []uintptr{s.secClassGenericPassword, service, accountRef, s.secUseAuthenticationUIFail}
	if includeValue {
		data, err := s.newData(value)
		if err != nil {
			s.cfRelease(accountRef)
			s.cfRelease(service)
			return 0, nil, err
		}
		owned = append(owned, data)
		keys = append(keys, s.secValueData)
		values = append(values, data)
	}
	dictionary, err := s.newDictionary(keys, values)
	if err != nil {
		for _, ref := range owned {
			s.cfRelease(ref)
		}
		return 0, nil, err
	}
	release := func() {
		s.cfRelease(dictionary)
		for _, ref := range owned {
			s.cfRelease(ref)
		}
	}
	return dictionary, release, nil
}

func (s *securityFrameworkKeychain) newString(value string) (uintptr, error) {
	const utf8Encoding = uint32(0x08000100)
	ref := s.cfStringCreate(0, value, utf8Encoding)
	if ref == 0 {
		return 0, fmt.Errorf("CFStringCreateWithCString failed")
	}
	return ref, nil
}

func (s *securityFrameworkKeychain) newData(value []byte) (uintptr, error) {
	var pointer *byte
	if len(value) > 0 {
		pointer = &value[0]
	}
	ref := s.cfDataCreate(0, pointer, int64(len(value)))
	if ref == 0 {
		return 0, fmt.Errorf("CFDataCreate failed")
	}
	return ref, nil
}

func (s *securityFrameworkKeychain) newDictionary(keys, values []uintptr) (uintptr, error) {
	if len(keys) == 0 || len(keys) != len(values) {
		return 0, fmt.Errorf("invalid CFDictionary entries")
	}
	ref := s.cfDictionaryCreate(0, &keys[0], &values[0], int64(len(keys)), s.dictionaryKeyCallbacks, s.dictionaryValueCallbacks)
	if ref == 0 {
		return 0, fmt.Errorf("CFDictionaryCreate failed")
	}
	return ref, nil
}

func classifySecurityStatus(status int32) error {
	switch status {
	case 0:
		return nil
	case -25300:
		return ErrNotFound
	case -25308, -25293, -128:
		return &BackendError{Kind: ErrBackendLocked, Backend: BackendKeychain}
	case -25299:
		return fmt.Errorf("keychain item already exists")
	default:
		return fmt.Errorf("Security.framework status %d", status)
	}
}
