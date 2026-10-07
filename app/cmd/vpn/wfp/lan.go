// Разрешение локальной сети (роутер, принтеры, ТВ, игры по LAN) — добавлено для приложения VPN.
// DNS в локальную сеть при этом остаётся запрещён (фильтр DNS весомее): имена не утекают провайдеру через роутер.

package wfp

import (
	"encoding/binary"
	"net/netip"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var lanV4 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32"}
var lanV6 = []string{"fe80::/10", "fc00::/7", "ff00::/8"}

func permitLAN(session uintptr, baseObjects *baseObjects, weight uint8) error {
	v4 := make([]wtFwpV4AddrAndMask, len(lanV4))
	conds4 := make([]wtFwpmFilterCondition0, len(lanV4))
	for i, s := range lanV4 {
		p := netip.MustParsePrefix(s)
		v4[i] = wtFwpV4AddrAndMask{addr: binary.BigEndian.Uint32(p.Addr().AsSlice()), mask: ^uint32(0) << (32 - p.Bits())}
		conds4[i] = wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_REMOTE_ADDRESS, matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{_type: cFWP_V4_ADDR_MASK, value: uintptr(unsafe.Pointer(&v4[i]))}}
	}
	v6 := make([]wtFwpV6AddrAndMask, len(lanV6))
	conds6 := make([]wtFwpmFilterCondition0, len(lanV6))
	for i, s := range lanV6 {
		p := netip.MustParsePrefix(s)
		v6[i] = wtFwpV6AddrAndMask{addr: p.Addr().As16(), prefixLength: uint8(p.Bits())}
		conds6[i] = wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_REMOTE_ADDRESS, matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{_type: cFWP_V6_ADDR_MASK, value: uintptr(unsafe.Pointer(&v6[i]))}}
	}
	add := func(name string, layer windows.GUID, conds []wtFwpmFilterCondition0) error {
		displayData, err := createWtFwpmDisplayData0(name, "")
		if err != nil {
			return wrapErr(err)
		}
		filter := wtFwpmFilter0{
			displayData:         *displayData,
			providerKey:         &baseObjects.provider,
			subLayerKey:         baseObjects.filters,
			layerKey:            layer,
			weight:              filterWeight(weight),
			numFilterConditions: uint32(len(conds)),
			filterCondition:     (*wtFwpmFilterCondition0)(unsafe.Pointer(&conds[0])),
			action:              wtFwpmAction0{_type: cFWP_ACTION_PERMIT},
		}
		filterID := uint64(0)
		return wrapErr(fwpmFilterAdd0(session, &filter, 0, &filterID))
	}
	for _, f := range []struct {
		name  string
		layer windows.GUID
		conds []wtFwpmFilterCondition0
	}{
		{"Permit LAN outbound (IPv4)", cFWPM_LAYER_ALE_AUTH_CONNECT_V4, conds4},
		{"Permit LAN inbound (IPv4)", cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4, conds4},
		{"Permit LAN outbound (IPv6)", cFWPM_LAYER_ALE_AUTH_CONNECT_V6, conds6},
		{"Permit LAN inbound (IPv6)", cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6, conds6},
	} {
		if err := add(f.name, f.layer, f.conds); err != nil {
			return err
		}
	}
	runtime.KeepAlive(v4)
	runtime.KeepAlive(v6)
	return nil
}

// permitApp разрешает весь трафик программы по пути к exe (условие ALE_APP_ID).
func permitApp(session uintptr, baseObjects *baseObjects, weight uint8, path string) error {
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var appID *wtFwpByteBlob
	if err := fwpmGetAppIdFromFileName0(p16, unsafe.Pointer(&appID)); err != nil {
		return wrapErr(err)
	}
	defer fwpmFreeMemory0(unsafe.Pointer(&appID))
	conds := []wtFwpmFilterCondition0{{fieldKey: cFWPM_CONDITION_ALE_APP_ID, matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{_type: cFWP_BYTE_BLOB_TYPE, value: uintptr(unsafe.Pointer(appID))}}}
	for _, layer := range []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4,
		cFWPM_LAYER_ALE_AUTH_CONNECT_V6, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6} {
		displayData, err := createWtFwpmDisplayData0("Permit app", path)
		if err != nil {
			return wrapErr(err)
		}
		filter := wtFwpmFilter0{displayData: *displayData, providerKey: &baseObjects.provider, subLayerKey: baseObjects.filters,
			layerKey: layer, weight: filterWeight(weight), numFilterConditions: 1,
			filterCondition: (*wtFwpmFilterCondition0)(unsafe.Pointer(&conds[0])), action: wtFwpmAction0{_type: cFWP_ACTION_PERMIT}}
		id := uint64(0)
		if err := fwpmFilterAdd0(session, &filter, 0, &id); err != nil {
			return wrapErr(err)
		}
	}
	runtime.KeepAlive(conds)
	return nil
}
