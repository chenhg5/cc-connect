import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { NativeItemView, WorkspaceHistory } from './WorkspaceChatItems';

describe('NativeItemView', () => {
  it('按 App Server 的 collabToolCall 类型渲染工具详情', () => {
    const view = render(<NativeItemView item={{
      id: 'item-1',
      type: 'collabToolCall',
      name: 'delegate_task',
      status: 'completed',
    }} />);

    expect(view.getByText('delegate_task')).toBeTruthy();
    expect(view.getByText('Tool details')).toBeTruthy();
  });

  it('把 pending interaction 插入对应的原生 item 后', () => {
    const view = render(<WorkspaceHistory
      turns={[{ id: 'turn-1', status: 'active' }]}
      itemsByTurn={{
        'turn-1': [{ turn_id: 'turn-1', item: { id: 'item-1', type: 'commandExecution', command: 'git status' } }],
      }}
      liveItems={{}}
      optimisticInputs={[]}
      nativeEvents={[]}
      interactions={[{
        id: 'interaction-1', kind: 'item/commandExecution/requestApproval', thread_id: 'thread-1',
        turn_id: 'turn-1', item_id: 'item-1', allowed_decisions: ['accept'], payload: {}, occurred_at: 'now',
      }]}
      renderInteraction={() => <div>Approval prompt</div>}
    />);

    const content = view.container.textContent || '';
    expect(content.indexOf('git status')).toBeGreaterThanOrEqual(0);
    expect(content.indexOf('Approval prompt')).toBeGreaterThan(content.indexOf('git status'));
    expect(view.getAllByText('Approval prompt')).toHaveLength(1);
  });
});
