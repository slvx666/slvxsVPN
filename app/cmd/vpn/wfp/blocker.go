/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2026 WireGuard LLC. All Rights Reserved.
 *
 * Пакет tunnel/firewall из wireguard-windows v1.1.1: «аварийный выключатель» на WFP. Фильтры живут в
 * динамической сессии — исчезают вместе с процессом, сеть не остаётся заблокированной после сбоя.
 * Изменения для VPN: названия, разрешение локальной сети (lan.go).
 */

package wfp

import (
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

type wfpObjectInstaller func(uintptr) error

// Fundamental WireGuard specific WFP objects.
type baseObjects struct {
	provider windows.GUID
	filters  windows.GUID
}

var wfpSession uintptr

func createWfpSession() (uintptr, error) {
	sessionDisplayData, err := createWtFwpmDisplayData0("VPN", "VPN kill switch (dynamic session)")
	if err != nil {
		return 0, wrapErr(err)
	}

	session := wtFwpmSession0{
		displayData:          *sessionDisplayData,
		flags:                cFWPM_SESSION_FLAG_DYNAMIC,
		txnWaitTimeoutInMSec: windows.INFINITE,
	}

	sessionHandle := uintptr(0)

	err = fwpmEngineOpen0(nil, cRPC_C_AUTHN_WINNT, nil, &session, unsafe.Pointer(&sessionHandle))
	if err != nil {
		return 0, wrapErr(err)
	}

	return sessionHandle, nil
}

func registerBaseObjects(session uintptr) (*baseObjects, error) {
	bo := &baseObjects{}
	var err error
	bo.provider, err = windows.GenerateGUID()
	if err != nil {
		return nil, wrapErr(err)
	}
	bo.filters, err = windows.GenerateGUID()
	if err != nil {
		return nil, wrapErr(err)
	}

	//
	// Register provider.
	//
	{
		displayData, err := createWtFwpmDisplayData0("VPN", "VPN kill switch provider")
		if err != nil {
			return nil, wrapErr(err)
		}
		provider := wtFwpmProvider0{
			providerKey: bo.provider,
			displayData: *displayData,
		}
		err = fwpmProviderAdd0(session, &provider, 0)
		if err != nil {
			// TODO: cleanup entire call chain of these if failure?
			return nil, wrapErr(err)
		}
	}

	//
	// Register filters sublayer.
	//
	{
		displayData, err := createWtFwpmDisplayData0("VPN filters", "Permissive and blocking filters")
		if err != nil {
			return nil, wrapErr(err)
		}
		sublayer := wtFwpmSublayer0{
			subLayerKey: bo.filters,
			displayData: *displayData,
			providerKey: &bo.provider,
			weight:      ^uint16(0),
		}
		err = fwpmSubLayerAdd0(session, &sublayer, 0)
		if err != nil {
			return nil, wrapErr(err)
		}
	}

	return bo, nil
}

func EnableFirewall(luid uint64, doNotRestrict bool, restrictToDNSServers []netip.Addr) error {
	session, err := createWfpSession()
	if err != nil {
		return wrapErr(err)
	}

	objectInstaller := func(session uintptr) error {
		baseObjects, err := registerBaseObjects(session)
		if err != nil {
			return wrapErr(err)
		}

		err = permitWireGuardService(session, baseObjects, 15)
		if err != nil {
			return wrapErr(err)
		}
		// пакеты, которые winws (WinDivert) возвращает в сеть, Windows проверяет повторно от имени «System»:
		// без этого с включённым обходом DPI не проходил весь прямой трафик (проверено замером 2026-10-07)
		if err := permitApp(session, baseObjects, 15, "System"); err != nil {
			return wrapErr(err)
		}

		if !doNotRestrict {
			if len(restrictToDNSServers) > 0 {
				err = blockDNS(restrictToDNSServers, session, baseObjects, 15, 14)
				if err != nil {
					return wrapErr(err)
				}
			}

			err = permitLoopback(session, baseObjects, 13)
			if err != nil {
				return wrapErr(err)
			}

			err = permitLAN(session, baseObjects, 12)
			if err != nil {
				return wrapErr(err)
			}

			err = permitTunInterface(session, baseObjects, 12, luid)
			if err != nil {
				return wrapErr(err)
			}

			err = permitDHCPIPv4(session, baseObjects, 12)
			if err != nil {
				return wrapErr(err)
			}

			err = permitDHCPIPv6(session, baseObjects, 12)
			if err != nil {
				return wrapErr(err)
			}

			err = permitNdp(session, baseObjects, 12)
			if err != nil {
				return wrapErr(err)
			}

			/* TODO: actually evaluate if this does anything and if we need this. It's layer 2; our other rules are layer 3.
			 *  In other words, if somebody complains, try enabling it. For now, keep it off.
			err = permitHyperV(session, baseObjects, 12)
			if err != nil {
				return wrapErr(err)
			}
			*/

			err = blockAll(session, baseObjects, 0)
			if err != nil {
				return wrapErr(err)
			}
		}

		return nil
	}

	err = runTransaction(session, objectInstaller)
	if err != nil {
		fwpmEngineClose0(session)
		return wrapErr(err)
	}

	// VPN: замена без «дыры» — новые фильтры уже действуют, только потом закрываем старую сессию
	old := wfpSession
	wfpSession = session
	if old != 0 {
		fwpmEngineClose0(old)
	}
	return nil
}

func DisableFirewall() {
	if wfpSession != 0 {
		fwpmEngineClose0(wfpSession)
		wfpSession = 0
	}
}

