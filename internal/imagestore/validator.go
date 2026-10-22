package imagestore

import (
	"buf.build/go/protovalidate"
	lotimage "github.com/Auction-Application/be-auction-item/rpc/gen/lot_image/v1"
)

func validate(generateUrlRequest *lotimage.GeneratePresignedUrlRequest) bool {
	err := protovalidate.Validate(generateUrlRequest)
	if err != nil {
		return false
	}
	return true
}
