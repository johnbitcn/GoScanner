//go:build darwin && cgo

package main

/*
#include "dnssd_darwin.h"
*/
import "C"

import (
	"context"
	"strings"
	"time"
	"unsafe"
)

func browseESCL(ctx context.Context) ([]dnssdService, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := C.scan_open()
	if b == nil {
		return nil, diagnosticf("Cannot allocate Bonjour discovery memory.", "Bonjour 内存分配失败")
	}
	defer C.scan_close(b)
	if b.error != 0 {
		return nil, diagnosticf("Cannot start Bonjour discovery: error %d.", "Bonjour 启动失败：%d", int(b.error))
	}
	deadline := time.Now().Add(discoveryWindow)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := C.scan_poll(b); err != 0 {
			return nil, diagnosticf("Bonjour discovery failed: error %d.", "Bonjour 发现失败：%d", int(err))
		}
	}
	var services []dnssdService
	for i := 0; i < int(b.count); i++ {
		s := &b.services[i]
		if s.active == 0 || s.ready == 0 {
			continue
		}
		service := dnssdService{Name: C.GoString(&s.name[0]), Address: C.GoString(&s.address[0]), Host: C.GoString(&s.host[0]), Port: int(s.port), Interface: int(s.iface), Secure: strings.HasPrefix(C.GoString(&s._type[0]), "_uscans."), TXT: C.GoBytes(unsafe.Pointer(&s.txt[0]), C.int(s.txt_len))}
		services = append(services, service)
	}
	return services, nil
}
