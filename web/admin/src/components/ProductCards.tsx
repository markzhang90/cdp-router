import { Collapse, Table, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import type { ProductCard } from "../types/admin";

function imageLink(value: string) {
  try {
    const url = new URL(value);
    if (url.protocol === "https:" || url.protocol === "http:") {
      return <Typography.Link href={url.href} target="_blank" rel="noopener noreferrer">查看图片</Typography.Link>;
    }
  } catch { /* Keep unrecognized provider values as text. */ }
  return <Typography.Text className="product-image-value" ellipsis={{ tooltip: true }}>{value || "—"}</Typography.Text>;
}

const columns: ColumnsType<ProductCard> = [
  { title: "商品名", dataIndex: "productName", width: 240, render: (value: string) => <Typography.Paragraph ellipsis={{ rows: 2, expandable: true }}>{value}</Typography.Paragraph> },
  { title: "商品 ID", dataIndex: "productId", width: 180, ellipsis: true },
  { title: "价格", dataIndex: "price", width: 100, ellipsis: true, render: (value: string) => value || "—" },
  { title: "销量", dataIndex: "salesVolume", width: 140, ellipsis: true, render: (value: string) => value || "—" },
  { title: "店铺", dataIndex: "shopName", width: 200, ellipsis: true },
  { title: "店铺 ID", dataIndex: "shopId", width: 160, ellipsis: true },
  { title: "卖点", dataIndex: "sellingPoints", width: 260, render: (value: string) => <Typography.Paragraph className="product-selling-points" ellipsis={{ rows: 2, expandable: true }}>{value || "—"}</Typography.Paragraph> },
  { title: "图片", dataIndex: "imageUrl", width: 100, render: imageLink }
];

export function ProductCards({ cards }: { cards: ProductCard[] }) {
  if (!cards.length) return <Typography.Text type="secondary">商品卡片：无</Typography.Text>;
  return <Collapse className="product-cards" items={[{
    key: "products",
    label: `商品卡片（${cards.length}）`,
    children: <Table<ProductCard> size="small" columns={columns} dataSource={cards} rowKey={(_, index) => String(index)} pagination={false} scroll={{ x: 1380 }} />
  }]} />;
}
