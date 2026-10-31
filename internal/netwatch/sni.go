package netwatch

import (
	"encoding/binary"
	"strings"
)

// SNIHost достаёт имя сайта (SNI) из TLS ClientHello (TLS-запись поверх TCP).
//
// isClientHello=true означает «это ClientHello», даже если SNI извлечь не
// удалось (ECH, обрезанный сегмент). Такой пакет не считается сбоем, но и
// не регистрируется как поток — ждать ответа не для чего.
func SNIHost(payload []byte) (host string, isClientHello bool) {
	// TLS record: type=handshake(0x16), version(2), length(2)
	if len(payload) < 5 || payload[0] != 0x16 {
		return "", false
	}
	recordLen := int(binary.BigEndian.Uint16(payload[3:5]))
	if recordLen == 0 {
		return "", true
	}
	if len(payload) < 5+recordLen {
		// ClientHello разрезан на несколько TCP-сегментов — детектор соберёт
		// его по sequence-номерам и спросит ещё раз
		return "", true
	}
	return ClientHelloSNI(payload[5:])
}

// ClientHelloSNI достаёт SNI из TLS Handshake ClientHello без TLS-записи —
// так он приходит внутри QUIC (CRYPTO-фрейм несёт handshake-сообщение целиком).
func ClientHelloSNI(hs []byte) (host string, isClientHello bool) {
	// handshake: type=client_hello(0x01), length(3)
	if len(hs) < 4 || hs[0] != 0x01 {
		return "", false
	}
	isClientHello = true
	off := 4
	if len(hs) < off+2+32 {
		return "", true
	}
	off += 2 + 32 // client_version + random

	if len(hs) < off+1 {
		return "", true
	}
	sidLen := int(hs[off])
	off++
	if off+sidLen > len(hs) {
		return "", true
	}
	off += sidLen

	if off+2 > len(hs) {
		return "", true
	}
	csLen := int(binary.BigEndian.Uint16(hs[off : off+2]))
	off += 2
	if off+csLen > len(hs) {
		return "", true
	}
	off += csLen

	if off+1 > len(hs) {
		return "", true
	}
	compLen := int(hs[off])
	off++
	if off+compLen > len(hs) {
		return "", true
	}
	off += compLen

	// extensions: total length(2), затем (type(2), len(2), data)
	if off+2 > len(hs) {
		return "", true
	}
	extLen := int(binary.BigEndian.Uint16(hs[off : off+2]))
	off += 2
	end := off + extLen
	if end > len(hs) {
		end = len(hs)
	}
	for off+4 <= end {
		extType := binary.BigEndian.Uint16(hs[off : off+2])
		l := int(binary.BigEndian.Uint16(hs[off+2 : off+4]))
		off += 4
		if off+l > end {
			break
		}
		if extType != 0 { // server_name
			off += l
			continue
		}
		return parseServerName(hs[off : off+l]), true
	}
	return "", true
}

// parseServerName разбирает extension server_name: list_len, затем
// записи (type, len, name); берём первую host_name (type 0).
func parseServerName(ext []byte) string {
	if len(ext) < 5 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(ext[0:2]))
	end := 2 + listLen
	if end > len(ext) {
		end = len(ext)
	}
	p := 2
	for p+3 <= end {
		nameType := ext[p]
		nameLen := int(binary.BigEndian.Uint16(ext[p+1 : p+3]))
		p += 3
		if p+nameLen > end {
			return ""
		}
		if nameType == 0 { // host_name
			return normalizeHost(string(ext[p : p+nameLen]))
		}
		p += nameLen
	}
	return ""
}

// normalizeHost приводит SNI к виду, пригодному для списков zapret.
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(h, ".")))
	if h == "" || len(h) > 253 {
		return ""
	}
	return h
}
