import type { Meta, StoryObj } from "@storybook/react-vite";
import { EmptyState, ErrorState, LoadingState, PermissionDenied } from "./states";

const meta = { title: "Primitives/States" } satisfies Meta;
export default meta;

export const Loading: StoryObj = { render: () => <LoadingState /> };
export const Empty: StoryObj = {
  render: () => (
    <EmptyState title="暂无数据" description="等待 Sub2API Connector（XM-0017）接入" />
  ),
};
export const ErrorCase: StoryObj = {
  render: () => <ErrorState message="上游返回 502" onRetry={() => {}} />,
};
export const Permission: StoryObj = {
  render: () => <PermissionDenied permission="ops.read" />,
};
