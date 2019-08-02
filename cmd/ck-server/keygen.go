package main

import (
	"crypto/rand"
	"github.com/cbeuw/Cloak/internal/ecdh"
)

func generateUID() string {
	UID := make([]byte, 16)
	rand.Read(UID)
	return b64(UID)
}

func generateKeyPair() (string, string) {
	staticPv, staticPub, _ := ecdh.GenerateKey(rand.Reader)
	marshPub := ecdh.Marshal(staticPub)
	marshPv := staticPv.(*[32]byte)[:]
	return b64(marshPub), b64(marshPv)
}
