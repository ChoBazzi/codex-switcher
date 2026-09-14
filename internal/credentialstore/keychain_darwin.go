//go:build darwin && cgo

package credentialstore

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

static CFMutableDictionaryRef query(const char *service) {
    CFMutableDictionaryRef q = CFDictionaryCreateMutable(NULL, 0,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFStringRef s = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
    CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(q, kSecAttrService, s);
    CFDictionarySetValue(q, kSecAttrAccount, CFSTR("registry"));
    CFRelease(s);
    return q;
}

static OSStatus read_secret(const char *service, void **out, long *length, long max_bytes) {
    CFMutableDictionaryRef q = query(service);
    CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
    CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(q, &result);
    CFRelease(q);
    if (status != errSecSuccess) return status;
    if (!result || CFGetTypeID(result) != CFDataGetTypeID()) {
        if (result) CFRelease(result);
        return errSecDecode;
    }
    CFDataRef data = (CFDataRef)result;
    *length = CFDataGetLength(data);
    if (*length <= 0 || *length > max_bytes) { CFRelease(result); return errSecDecode; }
    *out = malloc(*length);
    if (!*out) { CFRelease(result); return errSecAllocate; }
    memcpy(*out, CFDataGetBytePtr(data), *length);
    CFRelease(result);
    return errSecSuccess;
}

static OSStatus write_secret(const char *service, const void *bytes, long length) {
    CFMutableDictionaryRef q = query(service);
    CFDataRef data = CFDataCreate(NULL, bytes, length);
    CFDictionarySetValue(q, kSecValueData, data);
    CFDictionarySetValue(q, kSecAttrSynchronizable, kCFBooleanFalse);
    OSStatus status = SecItemAdd(q, NULL);
    if (status == errSecDuplicateItem) {
        CFDictionaryRemoveValue(q, kSecValueData);
        CFDictionaryRemoveValue(q, kSecAttrSynchronizable);
        CFMutableDictionaryRef changes = CFDictionaryCreateMutable(NULL, 0,
            &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
        CFDictionarySetValue(changes, kSecValueData, data);
        status = SecItemUpdate(q, changes);
        CFRelease(changes);
    }
    CFRelease(data);
    CFRelease(q);
    return status;
}

static OSStatus delete_secret(const char *service) {
    CFMutableDictionaryRef q = query(service);
    OSStatus status = SecItemDelete(q);
    CFRelease(q);
    return status;
}

static void clear_secret(void *data, long length) {
    volatile unsigned char *p = data;
    while (length-- > 0) *p++ = 0;
    free(data);
}
*/
import "C"

import "unsafe"

func (k *Keychain) Read() ([]byte, error) {
	s := C.CString(k.service)
	defer C.free(unsafe.Pointer(s))
	var data unsafe.Pointer
	var length C.long
	// Keep the native allocation/read boundary identical to the Go write limit.
	status := C.read_secret(s, &data, &length, C.long(MaxBytes))
	if status == C.errSecItemNotFound {
		return nil, ErrNotFound
	}
	if status != C.errSecSuccess {
		return nil, ErrUnavailable
	}
	defer C.clear_secret(data, length)
	return C.GoBytes(data, C.int(length)), nil
}

func (k *Keychain) Write(data []byte) error {
	if len(data) == 0 || len(data) > MaxBytes {
		return ErrInvalid
	}
	s := C.CString(k.service)
	defer C.free(unsafe.Pointer(s))
	if C.write_secret(s, unsafe.Pointer(&data[0]), C.long(len(data))) != C.errSecSuccess {
		return ErrUnavailable
	}
	return nil
}

// Only used to clean up the uniquely named synthetic integration test item.
func (k *Keychain) delete() error {
	s := C.CString(k.service)
	defer C.free(unsafe.Pointer(s))
	status := C.delete_secret(s)
	if status != C.errSecSuccess && status != C.errSecItemNotFound {
		return ErrUnavailable
	}
	return nil
}
