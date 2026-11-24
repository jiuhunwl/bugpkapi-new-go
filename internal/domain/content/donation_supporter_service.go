package content

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const donationTimeLayout = "2006-01-02 15:04:05"

var donationAmountPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,2})?$`)

func (s *Service) PublicDonationSupporters(ctx context.Context, params ListParams) (PageResult[DonationSupporterItem], error) {
	if err := validateListParams(&params); err != nil {
		return PageResult[DonationSupporterItem]{}, err
	}
	result, err := s.repo.ListDonationSupporters(ctx, params)
	if err != nil {
		return PageResult[DonationSupporterItem]{}, err
	}
	for index := range result.Items {
		result.Items[index].CreatorName = ""
	}
	return result, nil
}

func (s *Service) AdminDonationSupporters(ctx context.Context, principal Principal, params ListParams) (PageResult[DonationSupporterItem], error) {
	if err := s.authz.Require(ctx, principal, "donation:supporter:list"); err != nil {
		return PageResult[DonationSupporterItem]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[DonationSupporterItem]{}, err
	}
	return s.repo.ListDonationSupporters(ctx, params)
}

func (s *Service) CreateDonationSupporter(ctx context.Context, principal Principal, req CreateDonationSupporterRequest) (DonationSupporterItem, error) {
	if err := s.authz.Require(ctx, principal, "donation:supporter:create"); err != nil {
		return DonationSupporterItem{}, err
	}
	item, err := normalizeDonationSupporter(req, time.Now())
	if err != nil {
		return DonationSupporterItem{}, err
	}
	id, err := s.repo.CreateDonationSupporter(ctx, item, principal.ID)
	if err != nil {
		return DonationSupporterItem{}, err
	}
	return s.repo.DonationSupporterByID(ctx, id)
}

func normalizeDonationSupporter(req CreateDonationSupporterRequest, now time.Time) (DonationSupporterItem, error) {
	now = now.In(businessclock.Location())
	nickname, err := boundedText("nickname", req.Nickname, 64, true)
	if err != nil {
		return DonationSupporterItem{}, err
	}
	message, err := boundedText("message", req.Message, 255, false)
	if err != nil {
		return DonationSupporterItem{}, err
	}
	avatarURL := strings.TrimSpace(req.AvatarURL)
	if avatarURL != "" && !validImageURL(avatarURL, "") {
		return DonationSupporterItem{}, apperrors.New(consts.StatusBadRequest, "avatarUrl is invalid")
	}
	amount, err := normalizeDonationAmount(req.Amount)
	if err != nil {
		return DonationSupporterItem{}, err
	}
	donatedAt := strings.TrimSpace(req.DonatedAt)
	if donatedAt == "" {
		donatedAt = now.Format(donationTimeLayout)
	} else {
		parsed, parseErr := time.ParseInLocation(donationTimeLayout, donatedAt, businessclock.Location())
		if parseErr != nil {
			return DonationSupporterItem{}, apperrors.New(consts.StatusBadRequest, "donatedAt is invalid")
		}
		donatedAt = parsed.Format(donationTimeLayout)
	}
	return DonationSupporterItem{
		Amount:    amount,
		AvatarURL: avatarURL,
		DonatedAt: donatedAt,
		Message:   message,
		Nickname:  nickname,
	}, nil
}

func normalizeDonationAmount(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || !donationAmountPattern.MatchString(value) {
		return "", apperrors.New(consts.StatusBadRequest, "amount is invalid")
	}
	parts := strings.SplitN(value, ".", 2)
	integer := strings.TrimLeft(parts[0], "0")
	if integer == "" {
		integer = "0"
	}
	if len(integer) > 10 {
		return "", apperrors.New(consts.StatusBadRequest, "amount is invalid")
	}
	fraction := "00"
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	cents, err := strconv.ParseInt(integer+fraction, 10, 64)
	if err != nil || cents <= 0 {
		return "", apperrors.New(consts.StatusBadRequest, "amount must be greater than zero")
	}
	return integer + "." + fraction, nil
}
