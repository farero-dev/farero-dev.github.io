//go:build darwin && cgo

package secret

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>

static CFStringRef cfstr(const char *s) {
	return CFStringCreateWithCString(kCFAllocatorDefault, s, kCFStringEncodingUTF8);
}

static CFMutableDictionaryRef query(const char *service, const char *account) {
	CFMutableDictionaryRef q = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFStringRef s = cfstr(service), a = cfstr(account);
	CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(q, kSecAttrService, s);
	CFDictionarySetValue(q, kSecAttrAccount, a);
	CFRelease(s);
	CFRelease(a);
	return q;
}

static OSStatus kc_get(const char *service, const char *account, CFDataRef *out) {
	CFMutableDictionaryRef q = query(service, account);
	CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef result = NULL;
	OSStatus st = SecItemCopyMatching(q, &result);
	CFRelease(q);
	if (st == errSecSuccess) *out = (CFDataRef)result;
	return st;
}

static OSStatus kc_set(const char *service, const char *account, const void *data, long len) {
	CFDataRef value = CFDataCreate(kCFAllocatorDefault, data, len);
	CFMutableDictionaryRef q = query(service, account);
	CFMutableDictionaryRef attrs = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFDictionarySetValue(attrs, kSecValueData, value);
	OSStatus st = SecItemUpdate(q, attrs);
	if (st == errSecItemNotFound) {
		CFDictionarySetValue(q, kSecValueData, value);
		CFStringRef label = cfstr("farero");
		CFDictionarySetValue(q, kSecAttrLabel, label);
		CFRelease(label);
		st = SecItemAdd(q, NULL);
	}
	CFRelease(attrs);
	CFRelease(q);
	CFRelease(value);
	return st;
}

static OSStatus kc_delete(const char *service, const char *account) {
	CFMutableDictionaryRef q = query(service, account);
	OSStatus st = SecItemDelete(q);
	CFRelease(q);
	return st;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Keychain stores secrets as generic passwords in the login keychain. Items
// are created by farerod itself, so the item ACL trusts only farerod and
// other programs get a confirmation dialog.
type Keychain struct {
	Service string
}

// NewKeychain returns a Keychain store for the given service name.
func NewKeychain(service string) *Keychain { return &Keychain{Service: service} }

// Get implements Store.
func (k *Keychain) Get(key string) (string, error) {
	svc, acct := C.CString(k.Service), C.CString(key)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	var data C.CFDataRef
	st := C.kc_get(svc, acct, &data)
	if st == C.errSecItemNotFound {
		return "", ErrNotFound
	}
	if st != C.errSecSuccess {
		return "", fmt.Errorf("keychain read %s: OSStatus %d", key, int(st))
	}
	defer C.CFRelease(C.CFTypeRef(data))
	n := C.CFDataGetLength(data)
	return C.GoStringN((*C.char)(unsafe.Pointer(C.CFDataGetBytePtr(data))), C.int(n)), nil
}

// Set implements Store.
func (k *Keychain) Set(key, value string) error {
	svc, acct := C.CString(k.Service), C.CString(key)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	b := []byte(value)
	var p unsafe.Pointer
	if len(b) > 0 {
		p = C.CBytes(b)
		defer C.free(p)
	}
	if st := C.kc_set(svc, acct, p, C.long(len(b))); st != C.errSecSuccess {
		return fmt.Errorf("keychain write %s: OSStatus %d", key, int(st))
	}
	return nil
}

// Delete implements Store.
func (k *Keychain) Delete(key string) error {
	svc, acct := C.CString(k.Service), C.CString(key)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acct))
	st := C.kc_delete(svc, acct)
	if st != C.errSecSuccess && st != C.errSecItemNotFound {
		return fmt.Errorf("keychain delete %s: OSStatus %d", key, int(st))
	}
	return nil
}
