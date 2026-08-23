import { fireEvent, render } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { NativeInteraction } from '@/api/workspaceChat';
import { WorkspaceInteraction } from './WorkspaceChatInteractions';

function interaction(kind: string, payload: unknown, allowedDecisions?: unknown[]): NativeInteraction {
  return {
    id: 'interaction-1',
    kind,
    thread_id: 'thread-1',
    turn_id: 'turn-1',
    item_id: 'item-1',
    allowed_decisions: allowedDecisions,
    payload,
    occurred_at: '2026-08-23T00:00:00Z',
  };
}

describe('WorkspaceInteraction', () => {
  it('按 requestUserInput schema 提交答案且 secret 使用密码输入', () => {
    const onRespond = vi.fn();
    const view = render(<WorkspaceInteraction
      interaction={interaction('item/tool/requestUserInput', {
        questions: [{ id: 'token', header: 'Token', question: 'Enter token', isSecret: true, options: null }],
      })}
      disabled={false}
      onRespond={onRespond}
    />);
    const input = view.container.querySelector('input[type="password"]') as HTMLInputElement;
    expect(input).toBeTruthy();
    fireEvent.change(input, { target: { value: 'secret-value' } });
    fireEvent.click(view.getByRole('button', { name: 'Submit answer' }));
    expect(onRespond).toHaveBeenCalledWith('interaction-1', {
      answers: { token: { answers: ['secret-value'] } },
    });
    expect(view.container.textContent).not.toContain('secret-value');
  });

  it('命令审批仅显示上游 availableDecisions', () => {
    const onRespond = vi.fn();
    const view = render(<WorkspaceInteraction
      interaction={interaction('item/commandExecution/requestApproval', {
        command: 'git status',
      }, ['accept', 'decline'])}
      disabled={false}
      onRespond={onRespond}
    />);
    expect(view.getByRole('button', { name: 'Allow' })).toBeTruthy();
    expect(view.getByRole('button', { name: 'Deny' })).toBeTruthy();
    expect(view.queryByRole('button', { name: 'Allow for session' })).toBeNull();
    fireEvent.click(view.getByRole('button', { name: 'Deny' }));
    expect(onRespond).toHaveBeenCalledWith('interaction-1', { decision: 'decline' });
  });

  it('结构化命令决定保持原值提交', () => {
    const onRespond = vi.fn();
    const decision = { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['git', 'status'] } };
    const view = render(<WorkspaceInteraction
      interaction={interaction('item/commandExecution/requestApproval', { command: 'git status' }, [decision, 'decline'])}
      disabled={false}
      onRespond={onRespond}
    />);

    fireEvent.click(view.getByRole('button', { name: 'acceptWithExecpolicyAmendment' }));
    expect(onRespond).toHaveBeenCalledWith('interaction-1', { decision });
  });

  it('MCP 拒绝与取消显式提交 null content', () => {
    const onRespond = vi.fn();
    const view = render(<WorkspaceInteraction
      interaction={interaction('mcpServer/elicitation/request', {
        message: 'Share region?',
        requestedSchema: { type: 'object', properties: {} },
      }, ['accept', 'decline', 'cancel'])}
      disabled={false}
      onRespond={onRespond}
    />);

    fireEvent.click(view.getByRole('button', { name: 'Decline' }));
    expect(onRespond).toHaveBeenLastCalledWith('interaction-1', { action: 'decline', content: null });
    fireEvent.click(view.getByRole('button', { name: 'Cancel' }));
    expect(onRespond).toHaveBeenLastCalledWith('interaction-1', { action: 'cancel', content: null });
  });

  it('未知 interaction kind 不按 payload 形状猜测为结构化提问', () => {
    const onRespond = vi.fn();
    const view = render(<WorkspaceInteraction
      interaction={interaction('vendor/custom/request', {
        questions: [{ id: 'q1', question: 'Should not be inferred' }],
      })}
      disabled={false}
      onRespond={onRespond}
    />);

    expect(view.getByText('vendor/custom/request')).toBeTruthy();
    expect(view.queryByRole('button', { name: 'Submit answer' })).toBeNull();
    expect(onRespond).not.toHaveBeenCalled();
  });
});
