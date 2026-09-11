package placement

import (
	"fmt"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var wireCapacity = map[placementpb.Capacity]Capacity{
	placementpb.Capacity_CAPACITY_UNSPECIFIED: CapacityUnknown,
	placementpb.Capacity_CAPACITY_UNKNOWN:     CapacityUnknown,
	placementpb.Capacity_CAPACITY_AVAILABLE:   CapacityAvailable,
	placementpb.Capacity_CAPACITY_EXHAUSTED:   CapacityExhausted,
	placementpb.Capacity_CAPACITY_UNAVAILABLE: CapacityUnavailable,
}

var wirePresence = map[placementpb.Presence]Presence{
	placementpb.Presence_PRESENCE_UNSPECIFIED:   "",
	placementpb.Presence_PRESENCE_PRESENT:       Present,
	placementpb.Presence_PRESENCE_MATERIALIZING: Materializing,
	placementpb.Presence_PRESENCE_ABSENT:        Absent,
}

// CandidateFromProto preserves missing/stale facts for explicit assessment, but
// rejects unsupported fields and enum values rather than guessing their meaning.
// Caller must bind tenant, destination cell, policy revisions and source identity.
func CandidateFromProto(in *placementpb.ObservedCandidate, verb Verb) (Candidate, error) {
	if in == nil || (verb != Ingest && verb != Serve) {
		return Candidate{}, fmt.Errorf("candidate and verb are required")
	}
	if err := rejectUnknownWire(in); err != nil {
		return Candidate{}, err
	}
	capacity, ok := wireCapacity[in.GetCapacity()]
	if !ok {
		return Candidate{}, fmt.Errorf("unsupported observed capacity")
	}
	presence, ok := wirePresence[in.GetPresence()]
	if !ok {
		return Candidate{}, fmt.Errorf("unsupported observed presence")
	}
	for _, timestamp := range []*timestamppb.Timestamp{in.GetObservedAt(), in.GetExpiresAt(), in.GetCommercialFacts().GetExpiresAt()} {
		if timestamp != nil && !timestamp.IsValid() {
			return Candidate{}, fmt.Errorf("invalid observation timestamp")
		}
	}
	out := Candidate{
		TenantID: in.GetTenantId(), ClusterID: in.GetClusterId(), NodeID: in.GetNodeId(), OwnerTenantID: in.GetOwnerTenantId(),
		Official: in.GetOfficial(), Region: in.GetRegion(), Capacity: capacity, Presence: presence, SourceFeasible: in.GetSourceFeasible(),
		BWAvailable: in.GetBwAvailable(), BWLimit: in.GetBwLimit(), CPUPercent: in.GetCpuPercent(), RAMUsed: in.GetRamUsed(), RAMMax: in.GetRamMax(),
		ObservedAt: observationTime(in.GetObservedAt()), ExpiresAt: observationTime(in.GetExpiresAt()), Charging: ChargingUnknown,
	}
	for _, allowed := range in.GetAllowedVerbs() {
		switch allowed {
		case placementpb.Verb_VERB_INGEST:
			out.AllowedVerbs = append(out.AllowedVerbs, Ingest)
		case placementpb.Verb_VERB_SERVE:
			out.AllowedVerbs = append(out.AllowedVerbs, Serve)
		default:
			return Candidate{}, fmt.Errorf("unsupported observed verb")
		}
	}
	if in.GetLocation() != nil {
		out.Location = &Coordinates{Latitude: in.GetLocation().GetLatitude(), Longitude: in.GetLocation().GetLongitude()}
	}
	if facts := in.GetCommercialFacts(); facts != nil {
		if err := ValidateCommercialPrices(facts); err != nil {
			return Candidate{}, err
		}
		charging, known := wireCharging[facts.GetCharging()]
		if !known && facts.GetCharging() != placementpb.Charging_CHARGING_UNSPECIFIED {
			return Candidate{}, fmt.Errorf("unsupported charging classification")
		}
		if known {
			out.Charging = charging
		}
		out.ChargingRevision, out.ChargingUntil = facts.GetRevision(), observationTime(facts.GetExpiresAt())
		price := facts.GetServePrice()
		prices := facts.GetServePrices()
		if verb == Ingest {
			price = facts.GetIngestPrice()
			prices = facts.GetIngestPrices()
		}
		if price != nil {
			out.Price = &Price{AmountMicros: price.GetAmountMicros(), Currency: price.GetCurrency(), Unit: price.GetUnit(), Revision: facts.GetRevision(), ExpiresAt: out.ChargingUntil}
		}
		for _, quote := range prices {
			out.Prices = append(out.Prices, Price{AmountMicros: quote.GetAmountMicros(), Currency: quote.GetCurrency(), Unit: quote.GetUnit(), Revision: facts.GetRevision(), ExpiresAt: out.ChargingUntil})
		}
	}
	return out, nil
}

func CandidateToProto(in Candidate, verb Verb) (*placementpb.ObservedCandidate, error) {
	if verb != Ingest && verb != Serve {
		return nil, fmt.Errorf("candidate verb is required")
	}
	out := &placementpb.ObservedCandidate{
		TenantId: in.TenantID, ClusterId: in.ClusterID, NodeId: in.NodeID, OwnerTenantId: in.OwnerTenantID,
		Official: in.Official, Region: in.Region, SourceFeasible: in.SourceFeasible,
		BwAvailable: in.BWAvailable, BwLimit: in.BWLimit, CpuPercent: in.CPUPercent, RamUsed: in.RAMUsed, RamMax: in.RAMMax,
		ObservedAt: observationTimestamp(in.ObservedAt), ExpiresAt: observationTimestamp(in.ExpiresAt),
	}
	switch in.Capacity {
	case "", CapacityUnknown:
		out.Capacity = placementpb.Capacity_CAPACITY_UNKNOWN
	case CapacityAvailable:
		out.Capacity = placementpb.Capacity_CAPACITY_AVAILABLE
	case CapacityExhausted:
		out.Capacity = placementpb.Capacity_CAPACITY_EXHAUSTED
	case CapacityUnavailable:
		out.Capacity = placementpb.Capacity_CAPACITY_UNAVAILABLE
	default:
		return nil, fmt.Errorf("unsupported observed capacity")
	}
	if in.Presence != "" && in.Presence != Present && in.Presence != Materializing && in.Presence != Absent {
		return nil, fmt.Errorf("unsupported observed presence")
	}
	out.Presence = enumToProto(in.Presence, wirePresence)
	for _, allowed := range in.AllowedVerbs {
		switch allowed {
		case Ingest:
			out.AllowedVerbs = append(out.AllowedVerbs, placementpb.Verb_VERB_INGEST)
		case Serve:
			out.AllowedVerbs = append(out.AllowedVerbs, placementpb.Verb_VERB_SERVE)
		default:
			return nil, fmt.Errorf("unsupported observed verb")
		}
	}
	if in.Location != nil {
		out.Location = &placementpb.Coordinates{Latitude: in.Location.Latitude, Longitude: in.Location.Longitude}
	}
	if in.Charging != "" && in.Charging != ChargingUnknown && in.Charging != Rated && in.Charging != PermanentlyFree {
		return nil, fmt.Errorf("unsupported charging classification")
	}
	if in.Charging == Rated || in.Charging == PermanentlyFree || in.Price != nil || len(in.Prices) != 0 || !in.ChargingUntil.IsZero() || in.ChargingRevision != "" {
		facts := &placementpb.CommercialFacts{Charging: enumToProto(in.Charging, wireCharging), Revision: in.ChargingRevision, ExpiresAt: observationTimestamp(in.ChargingUntil)}
		if in.Price != nil {
			if in.Price.Revision != in.ChargingRevision || !in.Price.ExpiresAt.Equal(in.ChargingUntil) {
				return nil, fmt.Errorf("price and charging facts have different revisions or expiry")
			}
			price := &placementpb.Price{AmountMicros: in.Price.AmountMicros, Currency: in.Price.Currency, Unit: in.Price.Unit}
			if verb == Ingest {
				facts.IngestPrice = price
			} else {
				facts.ServePrice = price
			}
		}
		for _, quote := range in.Prices {
			if quote.Revision != in.ChargingRevision || !quote.ExpiresAt.Equal(in.ChargingUntil) {
				return nil, fmt.Errorf("price and charging facts have different revisions or expiry")
			}
			price := &placementpb.Price{AmountMicros: quote.AmountMicros, Currency: quote.Currency, Unit: quote.Unit}
			if verb == Ingest {
				facts.IngestPrices = append(facts.IngestPrices, price)
			} else {
				facts.ServePrices = append(facts.ServePrices, price)
			}
		}
		out.CommercialFacts = facts
	}
	if _, err := CandidateFromProto(out, verb); err != nil {
		return nil, err
	}
	return out, nil
}

func observationTime(timestamp *timestamppb.Timestamp) time.Time {
	if timestamp == nil {
		return time.Time{}
	}
	return timestamp.AsTime()
}

func observationTimestamp(instant time.Time) *timestamppb.Timestamp {
	if instant.IsZero() {
		return nil
	}
	return timestamppb.New(instant)
}
