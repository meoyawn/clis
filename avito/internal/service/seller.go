package service

import (
	"regexp"
	"strconv"
	"strings"
)

// Search cards expose score and a localized summary. Listing pages expose
// scoreFloat and activeReviewsCount. Keep missing values distinct from zero.
type webSellerRating struct {
	Score              *float64 `json:"score"`
	ScoreFloat         *float64 `json:"scoreFloat"`
	ActiveReviewsCount *int64   `json:"activeReviewsCount"`
	Summary            string   `json:"summary"`
}

type webSellerInfo struct {
	ComponentData struct {
		Component string `json:"component"`
	} `json:"componentData"`
	Payload struct {
		Rating  *webSellerRating `json:"rating"`
		Profile struct {
			Title string `json:"title"`
		} `json:"profile"`
	} `json:"payload"`
}

func (rating *webSellerRating) seller(name string) Seller {
	seller := Seller{Name: plainText(name)}
	if rating == nil {
		return seller
	}
	seller.Rating = rating.ScoreFloat
	if seller.Rating == nil {
		seller.Rating = rating.Score
	}
	if seller.Rating != nil && (*seller.Rating < 0 || *seller.Rating > 5) {
		seller.Rating = nil
	}
	seller.RatingsSummary = plainText(rating.Summary)
	seller.NumRatings = rating.ActiveReviewsCount
	if seller.NumRatings != nil && *seller.NumRatings < 0 {
		seller.NumRatings = nil
	}
	if seller.NumRatings == nil {
		seller.NumRatings = exactReviewCount(seller.RatingsSummary)
	}
	return seller
}

var reviewCountPattern = regexp.MustCompile(`^([0-9]+(?: [0-9]{3})*) (?:отзыв|отзыва|отзывов|оценка|оценки|оценок|review|reviews|rating|ratings)$`)

func exactReviewCount(summary string) *int64 {
	// Normalize ordinary, non-breaking, and narrow non-breaking spaces. Do not
	// turn rounded summaries such as "1,2 тыс. отзывов" into an exact count.
	text := strings.ToLower(strings.Join(strings.Fields(summary), " "))
	if text == "нет отзывов" || text == "нет оценок" || text == "no reviews" || text == "no ratings" {
		count := int64(0)
		return &count
	}
	match := reviewCountPattern.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	count, err := strconv.ParseInt(strings.ReplaceAll(match[1], " ", ""), 10, 64)
	if err != nil {
		return nil
	}
	return &count
}

func (row webListing) seller() Seller {
	rating := row.Rating
	name := ""
	for _, info := range row.IVA.UserInfoStep {
		if info.ComponentData.Component != "seller-info" {
			continue
		}
		name = info.Payload.Profile.Title
		if rating == nil {
			rating = info.Payload.Rating
		}
		break
	}
	return rating.seller(name)
}
