import { Button, Typography } from "antd";
import { Copy } from "lucide-react";
import { prettyJSON } from "../utils/format";

interface JsonBlockProps {
  value: string;
  maxHeight?: number;
}

export function JsonBlock({ value, maxHeight = 280 }: JsonBlockProps) {
  const text = prettyJSON(value);

  return (
    <div className="json-block" style={{ maxHeight }}>
      <Button
        className="json-copy"
        icon={<Copy size={15} />}
        size="small"
        type="text"
        onClick={() => navigator.clipboard.writeText(text)}
      />
      <Typography.Text>
        <pre>{text}</pre>
      </Typography.Text>
    </div>
  );
}
