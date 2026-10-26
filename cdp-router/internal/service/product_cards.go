package service

import (
	taskv1 "github.com/GitMarkZhang/cdp-router/api/task/v1"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
)

func productCardsProto(cards []biz.ProductCard) []*taskv1.ProductCard {
	out := make([]*taskv1.ProductCard, 0, len(cards))
	for _, card := range cards {
		out = append(out, &taskv1.ProductCard{
			ProductId: card.ProductID, ProductName: card.ProductName, Price: card.Price,
			ShopName: card.ShopName, ShopId: card.ShopID, SellingPoints: card.SellingPoints,
			ImageUrl: card.ImageURL, SalesVolume: card.SalesVolume,
		})
	}
	return out
}
