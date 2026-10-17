package faxsvc

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// EFax receives eFax Developer's inbound posts: each fax is a form POST whose "xml" field is an InboundPostRequest,
// with the user name and password set in the eFax account in its AccessControl, the fax base64 in FileContents (PDF or
// TIFF, as FileType says), and the sender's number in ANI. eFax expects the body "Post Successful" back, and posts the
// fax again until it gets it. Written from eFax Developer's inbound post format as its public pages describe it and an
// open-source client library reproduces it, and tested against posts built from that; not tested with eFax itself.
// eFax Corporate's newer Enterprise API is not here.
type EFax struct {
	User, Password string
	Store          Store
}

type efaxPost struct {
	XMLName       xml.Name `xml:"InboundPostRequest"`
	AccessControl struct {
		UserName string `xml:"UserName"`
		Password string `xml:"Password"`
	} `xml:"AccessControl"`
	RequestControl struct {
		RequestType string `xml:"RequestType"`
	} `xml:"RequestControl"`
	FaxControl struct {
		AccountID    string `xml:"AccountID"`
		FileType     string `xml:"FileType"`
		PageCount    string `xml:"PageCount"`
		CSID         string `xml:"CSID"`
		ANI          string `xml:"ANI"`
		MCFID        string `xml:"MCFID"`
		FileContents string `xml:"FileContents"`
	} `xml:"FaxControl"`
}

func (e *EFax) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxFax*2)
	raw := r.PostFormValue("xml")
	if raw == "" {
		// Some set-ups post the XML as the body itself.
		b, _ := io.ReadAll(io.LimitReader(r.Body, MaxFax*2))
		raw = string(b)
	}
	var p efaxPost
	if err := xml.Unmarshal([]byte(raw), &p); err != nil {
		http.Error(w, "no InboundPostRequest", http.StatusBadRequest)
		return
	}
	u, pw := p.AccessControl.UserName, p.AccessControl.Password
	if e.Password == "" || subtle.ConstantTimeCompare([]byte(pw), []byte(e.Password)) != 1 ||
		subtle.ConstantTimeCompare([]byte(u), []byte(e.User)) != 1 {
		http.Error(w, "the eFax account's posting credentials are needed", http.StatusUnauthorized)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(p.RequestControl.RequestType), "New Inbound") {
		io.WriteString(w, "Post Successful") // another kind of post: acknowledged, nothing to keep
		return
	}
	fax, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(p.FaxControl.FileContents), ""))
	if err != nil || len(fax) == 0 || len(fax) > MaxFax {
		http.Error(w, "FileContents is not a base64 fax of 8 MB or less", http.StatusBadRequest)
		return
	}
	ct := sniff(fax)
	if ct == "" {
		switch strings.ToLower(strings.TrimSpace(p.FaxControl.FileType)) {
		case "pdf":
			ct = "application/pdf"
		case "tif", "tiff":
			ct = "image/tiff"
		}
	}
	pages, _ := strconv.Atoi(strings.TrimSpace(p.FaxControl.PageCount))
	from := strings.TrimSpace(p.FaxControl.ANI)
	if from == "" {
		from = strings.TrimSpace(p.FaxControl.CSID)
	}
	if err := e.Store(r.Context(), fax, ct, from, pages, "fax-service:efax"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, "Post Successful")
}
