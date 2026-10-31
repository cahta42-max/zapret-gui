package netwatch

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// QUIC Initial: расшифровка ClientHello.
//
// QUIC Initial шифруется ключами, выводимыми из DCID первого пакета
// (RFC 9001 §5.2) — то есть смотреть трафик можно пассивно, ничего не зная
// заранее. Из расшифрованного CRYPTO-фрейма достаём TLS ClientHello и SNI,
// как в TCP-детекторе. Поддержаны QUIC v1 и v2; draft-версии — по правилам v1.
//
// Не расшифровалось (0-RTT, Handshake, ключи обновлены, расширение header
// protection не сошлось) — просто пропускаем: ложных событий не порождаем.

const (
	quicVersion1 = 0x00000001
	quicVersion2 = 0x6b3343cf

	// размер тега AES-GCM
	quicTagLen = 16
)

var (
	quicSaltV1 = mustHex("38762cf7f55934b34d179ae6a4c80cadccbb7f0a")
	quicSaltV2 = mustHex("0dede3def700a6db819381be6e269dcbf9bd2ed9")
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("netwatch: плохой hex: " + s)
	}
	return b
}

// ParseQUICInitial расшифровывает один датаграмм QUIC Initial и возвращает
// данные CRYPTO-фрейма (поток TLS handshake) и его смещение в потоке.
// ok=false — это не Initial или расшифровать не удалось.
func ParseQUICInitial(d []byte) (cryptoData []byte, cryptoOffset uint64, ok bool) {
	if len(d) < 32 {
		return nil, 0, false
	}
	first := d[0]
	if first&0x80 == 0 { // short header: не Initial
		return nil, 0, false
	}
	version := binary.BigEndian.Uint32(d[1:5])
	if version == 0 { // Version Negotiation
		return nil, 0, false
	}
	salt := quicSaltV1
	initialType := byte(0x00)
	switch version {
	case quicVersion1:
		initialType = 0x00
	case quicVersion2:
		initialType = 0x01
		salt = quicSaltV2
	default:
		if version&0xff000000 != 0xff000000 {
			return nil, 0, false // не draft-версия — не знаем, как расшифровать
		}
	}
	if (first>>4)&0x03 != initialType {
		return nil, 0, false // Handshake/0-RTT/Retry
	}

	off := 5
	dcidLen := int(d[off])
	off++
	if dcidLen < 1 || dcidLen > 20 || off+dcidLen > len(d) {
		return nil, 0, false
	}
	dcid := d[off : off+dcidLen]
	off += dcidLen
	if off >= len(d) {
		return nil, 0, false
	}
	scidLen := int(d[off])
	off++
	if scidLen > 20 || off+scidLen > len(d) {
		return nil, 0, false
	}
	off += scidLen

	tokenLen, n, ok := quicVarint(d[off:])
	if !ok {
		return nil, 0, false
	}
	off += n
	if tokenLen > uint64(len(d)-off) {
		return nil, 0, false
	}
	off += int(tokenLen)

	length, n, ok := quicVarint(d[off:])
	if !ok {
		return nil, 0, false
	}
	off += n
	pnOffset := off
	if pnOffset+4+quicTagLen > len(d) {
		return nil, 0, false
	}
	payloadEnd := pnOffset + int(length)
	if payloadEnd > len(d) || payloadEnd < pnOffset+1+quicTagLen {
		return nil, 0, false
	}

	key, iv, hp := deriveInitialKeys(salt, dcid)

	// снятие header protection: sample — 16 байт после (незащищённого) PN
	sample := d[pnOffset+4 : pnOffset+4+quicTagLen]
	mask := aesECBFirst5(hp, sample)
	pnLen := int((first^mask[0])&0x03) + 1
	if pnOffset+pnLen > len(d) {
		return nil, 0, false
	}
	header := make([]byte, pnOffset+pnLen)
	copy(header, d[:pnOffset+pnLen])
	header[0] = first ^ (mask[0] & 0x0f)
	var pn uint64
	for i := 0; i < pnLen; i++ {
		b := d[pnOffset+i] ^ mask[1+i]
		header[pnOffset+i] = b
		pn = pn<<8 | uint64(b)
	}
	if pnOffset+pnLen > payloadEnd {
		return nil, 0, false
	}
	ciphertext := d[pnOffset+pnLen : payloadEnd]
	if len(ciphertext) < quicTagLen {
		return nil, 0, false
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, false
	}
	nonce := make([]byte, 12)
	copy(nonce, iv)
	for i := 0; i < 8; i++ { // PN в младшие 8 байт nonce
		nonce[11-i] ^= byte(pn >> (8 * i))
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return nil, 0, false
	}
	return parseCryptoFrames(plain)
}

// deriveInitialKeys — клиентские key/iv/hp из соли и DCID (RFC 9001 §5.2).
func deriveInitialKeys(salt, dcid []byte) (key, iv, hp []byte) {
	initialSecret, err := hkdf.Extract(sha256.New, dcid, salt)
	if err != nil {
		return nil, nil, nil
	}
	clientSecret, err := hkdf.Expand(sha256.New, initialSecret, "client in", 32)
	if err != nil {
		return nil, nil, nil
	}
	key, _ = hkdf.Expand(sha256.New, clientSecret, "quic key", 16)
	iv, _ = hkdf.Expand(sha256.New, clientSecret, "quic iv", 12)
	hp, _ = hkdf.Expand(sha256.New, clientSecret, "quic hp", 16)
	return key, iv, hp
}

// aesECBFirst5 — первые 5 байт AES-128-ECB(hpKey, sample) — маска header protection.
func aesECBFirst5(hpKey, sample []byte) [5]byte {
	var out [5]byte
	if len(hpKey) != 16 || len(sample) != 16 {
		return out
	}
	block, err := aes.NewCipher(hpKey)
	if err != nil {
		return out
	}
	var enc [16]byte
	block.Encrypt(enc[:], sample)
	copy(out[:], enc[:5])
	return out
}

// parseCryptoFrames разбирает plaintext Initial: находит CRYPTO-фрейм (0x06).
func parseCryptoFrames(plain []byte) ([]byte, uint64, bool) {
	off := 0
	for off < len(plain) {
		frameType, n, ok := quicVarint(plain[off:])
		if !ok {
			return nil, 0, false
		}
		off += n
		switch frameType {
		case 0x00, 0x01: // PADDING, PING
			continue
		case 0x06: // CRYPTO: offset, length, data
			cryptoOff, n1, ok := quicVarint(plain[off:])
			if !ok {
				return nil, 0, false
			}
			off += n1
			cryptoLen, n2, ok := quicVarint(plain[off:])
			if !ok {
				return nil, 0, false
			}
			off += n2
			if cryptoLen > uint64(len(plain)-off) {
				return nil, 0, false
			}
			return plain[off : off+int(cryptoLen)], cryptoOff, true
		case 0x02, 0x03: // ACK: largest_ack, ack_delay, range_count, first_range, [gap, range]*
			if _, n, ok := quicVarint(plain[off:]); !ok {
				return nil, 0, false
			} else {
				off += n
			}
			if _, n, ok := quicVarint(plain[off:]); !ok {
				return nil, 0, false
			} else {
				off += n
			}
			rangeCount, n, ok := quicVarint(plain[off:])
			if !ok {
				return nil, 0, false
			}
			off += n
			if _, n, ok := quicVarint(plain[off:]); !ok {
				return nil, 0, false
			} else {
				off += n
			}
			for i := uint64(0); i < rangeCount; i++ {
				if _, n, ok := quicVarint(plain[off:]); !ok {
					return nil, 0, false
				} else {
					off += n
				}
				if _, n, ok := quicVarint(plain[off:]); !ok {
					return nil, 0, false
				} else {
					off += n
				}
			}
		default:
			// прочие фреймы в Initial не встречаются — дальше не разбираем
			return nil, 0, false
		}
	}
	return nil, 0, false
}

// quicVarint — QUIC varint (RFC 9000 §16) и длина его префикса.
func quicVarint(b []byte) (uint64, int, bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	switch b[0] >> 6 {
	case 0:
		return uint64(b[0] & 0x3f), 1, true
	case 1:
		if len(b) < 2 {
			return 0, 0, false
		}
		return uint64(binary.BigEndian.Uint16(b[:2]) & 0x3fff), 2, true
	case 2:
		if len(b) < 4 {
			return 0, 0, false
		}
		return uint64(binary.BigEndian.Uint32(b[:4]) & 0x3fffffff), 4, true
	default:
		if len(b) < 8 {
			return 0, 0, false
		}
		return binary.BigEndian.Uint64(b[:8]) & 0x3fffffffffffffff, 8, true
	}
}
