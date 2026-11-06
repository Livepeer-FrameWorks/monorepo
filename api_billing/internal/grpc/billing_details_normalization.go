package grpc

import (
	"strings"

	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func normalizeBillingDetailsUpdate(request *purserpb.UpdateBillingDetailsRequest) *purserpb.UpdateBillingDetailsRequest {
	normalized := proto.CloneOf(request)
	for _, field := range []*string{normalized.Name, normalized.Company, normalized.Email, normalized.VatNumber, normalized.CustomerType} {
		if field != nil {
			*field = strings.TrimSpace(*field)
		}
	}
	if address := normalized.Address; address != nil {
		address.Street = strings.TrimSpace(address.Street)
		address.City = strings.TrimSpace(address.City)
		address.State = strings.TrimSpace(address.State)
		address.PostalCode = strings.TrimSpace(address.PostalCode)
		address.Country = countries.Normalize(address.Country)
	}
	return normalized
}

func normalizeBillingUpdateVAT(request *purserpb.UpdateBillingDetailsRequest, savedAddress []byte) error {
	if request.VatNumber == nil || *request.VatNumber == "" {
		return nil
	}
	address := request.Address
	if address == nil {
		address = scanBillingAddress(savedAddress)
	}
	if address == nil || !billingtax.IsEU(address.Country) {
		return nil
	}
	canonical, err := billingtax.CanonicalVATNumber(address.Country, *request.VatNumber)
	if err != nil {
		return status.Error(codes.InvalidArgument, "VAT number must match the billing country")
	}
	*request.VatNumber = canonical
	return nil
}
