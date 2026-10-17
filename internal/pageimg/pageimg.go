// Package pageimg turns a scanned document into one picture per page, for a reader that reads pictures (tesseract, a
// model that reads images): PNG and JPEG as they are; GIF, and TIFF as fax services and scanners write it (CCITT
// Group 3 or 4, PackBits or none; bilevel or 8-bit grey), as PNG; and from a PDF, the images on its pages
// (CCITT, JPEG, or Flate-compressed grey, RGB or bilevel). It reads no PDF page's drawing: a PDF of typed text has
// its text read by pdftotext instead. JBIG2, JPEG 2000 and LZW images, and encrypted PDFs, are not read.
package pageimg

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/png"
)

// Page is one page's picture.
type Page struct {
	ContentType string // image/png or image/jpeg
	Data        []byte
}

// MaxPages bounds the pages taken from one document.
const MaxPages = 30

// Pages is a document's pages as pictures.
func Pages(contentType string, raw []byte) ([]Page, error) {
	switch {
	case contentType == "image/png" || bytes.HasPrefix(raw, []byte("\x89PNG")):
		return []Page{{"image/png", raw}}, nil
	case contentType == "image/jpeg" || bytes.HasPrefix(raw, []byte("\xff\xd8\xff")):
		return []Page{{"image/jpeg", raw}}, nil
	case contentType == "image/gif" || bytes.HasPrefix(raw, []byte("GIF8")):
		img, err := gif.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		p, err := encodePNG(img)
		return []Page{p}, err
	case contentType == "image/tiff" || bytes.HasPrefix(raw, []byte("II*\x00")) || bytes.HasPrefix(raw, []byte("MM\x00*")):
		return tiffPages(raw)
	case contentType == "application/pdf" || bytes.HasPrefix(raw, []byte("%PDF")):
		return pdfPages(raw)
	}
	return nil, fmt.Errorf("%s is not a picture this server can read", contentType)
}

func encodePNG(img image.Image) (Page, error) {
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, img); err != nil {
		return Page{}, err
	}
	return Page{"image/png", b.Bytes()}, nil
}

var errNoImages = errors.New("no page pictures in it")
