import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, HelpCircle, ShieldAlert } from 'lucide-react';
import { Button } from '@/components/ui';
import type { NativeInteraction } from '@/api/workspaceChat';
import { JsonDetails } from './WorkspaceChatItems';

const asRecord = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;
const asString = (value: unknown): string => typeof value === 'string' ? value : '';

interface InteractionProps {
  interaction: NativeInteraction;
  disabled: boolean;
  onRespond: (interactionId: string, response: unknown) => void;
}

interface RequestQuestion {
  id: string;
  header: string;
  question: string;
  isOther: boolean;
  isSecret: boolean;
  options: { label: string; description: string }[];
}

function requestQuestions(payload: Record<string, unknown>): RequestQuestion[] {
  if (!Array.isArray(payload.questions)) return [];
  return payload.questions.map(asRecord).filter(Boolean).map((question) => ({
    id: asString(question?.id),
    header: asString(question?.header),
    question: asString(question?.question),
    isOther: question?.isOther === true,
    isSecret: question?.isSecret === true,
    options: Array.isArray(question?.options)
      ? question.options.map(asRecord).filter(Boolean).map((option) => ({
          label: asString(option?.label),
          description: asString(option?.description),
        }))
      : [],
  })).filter((question) => question.id && question.question);
}

function UserInputInteraction({ interaction, disabled, onRespond }: InteractionProps) {
  const { t } = useTranslation();
  const payload = asRecord(interaction.payload) || {};
  const questions = useMemo(() => requestQuestions(payload), [interaction.payload]);
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const complete = questions.length > 0 && questions.every((question) => (answers[question.id] || '').trim());
  const submit = () => {
    const response = Object.fromEntries(questions.map((question) => [question.id, { answers: [(answers[question.id] || '').trim()] }]));
    onRespond(interaction.id, { answers: response });
  };
  return (
    <InteractionShell icon={<HelpCircle size={16} />} title={t('workspaceChat.inputRequired')} payload={interaction.payload}>
      <div className="space-y-4">
        {questions.map((question) => (
          <fieldset key={question.id} className="min-w-0">
            <legend className="text-sm font-medium text-gray-900 dark:text-white">{question.header || question.question}</legend>
            {question.header && <p className="mt-1 text-xs text-gray-500 dark:text-gray-400">{question.question}</p>}
            {question.options.length > 0 && (
              <div className="mt-2 space-y-1.5">
                {question.options.map((option) => (
                  <label key={option.label} className="flex cursor-pointer items-start gap-2 rounded-md border border-gray-200 px-3 py-2 text-sm dark:border-white/[0.1]">
                    <input type="radio" name={`${interaction.id}-${question.id}`} value={option.label} checked={answers[question.id] === option.label} onChange={() => setAnswers((current) => ({ ...current, [question.id]: option.label }))} className="mt-0.5 accent-current" />
                    <span className="min-w-0"><span className="block break-words">{option.label}</span>{option.description && <span className="block break-words text-xs text-gray-500">{option.description}</span>}</span>
                  </label>
                ))}
              </div>
            )}
            {(question.options.length === 0 || question.isOther) && (
              <input
                type={question.isSecret ? 'password' : 'text'}
                autoComplete="off"
                value={question.options.some((option) => option.label === answers[question.id]) ? '' : answers[question.id] || ''}
                onChange={(event) => setAnswers((current) => ({ ...current, [question.id]: event.target.value }))}
                placeholder={question.isOther ? t('workspaceChat.otherAnswer') : t('workspaceChat.answer')}
                className="mt-2 w-full min-w-0 rounded-md border border-gray-300 bg-white px-3 py-2 text-sm outline-none focus:border-accent focus:ring-2 focus:ring-accent/30 dark:border-white/[0.12] dark:bg-black/30"
              />
            )}
          </fieldset>
        ))}
        <Button size="sm" disabled={disabled || !complete} onClick={submit}><Check size={14} />{t('workspaceChat.submitAnswer')}</Button>
      </div>
    </InteractionShell>
  );
}

function decisionLabel(value: unknown): string {
  if (typeof value === 'string') return value;
  const record = asRecord(value);
  return record ? Object.keys(record)[0] || JSON.stringify(record) : String(value);
}

function PermissionInteraction({ interaction, disabled, onRespond }: InteractionProps) {
  const { t } = useTranslation();
  const payload = asRecord(interaction.payload) || {};
  const permissions = payload.permissions;
  return (
    <InteractionShell icon={<ShieldAlert size={16} />} title={t('workspaceChat.permissionRequest')} payload={interaction.payload}>
      {asString(payload.reason) && <p className="mb-3 whitespace-pre-wrap break-words text-sm">{asString(payload.reason)}</p>}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" disabled={disabled} onClick={() => onRespond(interaction.id, { permissions, scope: 'turn' })}>{t('workspaceChat.allowTurn')}</Button>
        <Button size="sm" variant="secondary" disabled={disabled} onClick={() => onRespond(interaction.id, { permissions, scope: 'session' })}>{t('workspaceChat.allowSession')}</Button>
        <Button size="sm" variant="danger" disabled={disabled} onClick={() => onRespond(interaction.id, { permissions: {}, scope: 'turn' })}>{t('workspaceChat.deny')}</Button>
      </div>
    </InteractionShell>
  );
}

interface JSONSchemaProperty {
  type?: string;
  title?: string;
  description?: string;
  enum?: string[];
}

function MCPInteraction({ interaction, disabled, onRespond }: InteractionProps) {
  const { t } = useTranslation();
  const payload = asRecord(interaction.payload) || {};
  const schema = asRecord(payload.requestedSchema);
  const properties = asRecord(schema?.properties) || {};
  const required = new Set(Array.isArray(schema?.required) ? schema.required.filter((value): value is string => typeof value === 'string') : []);
  const [values, setValues] = useState<Record<string, unknown>>({});
  const complete = [...required].every((key) => values[key] !== undefined && values[key] !== '');
  return (
    <InteractionShell icon={<HelpCircle size={16} />} title={asString(payload.message) || t('workspaceChat.mcpRequest')} payload={interaction.payload}>
      <div className="space-y-3">
        {Object.entries(properties).map(([key, raw]) => {
          const property = (asRecord(raw) || {}) as JSONSchemaProperty;
          const label = property.title || key;
          if (Array.isArray(property.enum)) {
            return <label key={key} className="block text-xs font-medium">{label}<select value={asString(values[key])} onChange={(event) => setValues((current) => ({ ...current, [key]: event.target.value }))} className="mt-1 w-full rounded-md border border-gray-300 bg-white px-2 py-2 text-sm dark:border-white/[0.12] dark:bg-black/30"><option value="">{t('workspaceChat.chooseOption')}</option>{property.enum.map((option) => <option key={option} value={option}>{option}</option>)}</select></label>;
          }
          if (property.type === 'boolean') {
            return <label key={key} className="flex items-center gap-2 text-sm"><input type="checkbox" checked={values[key] === true} onChange={(event) => setValues((current) => ({ ...current, [key]: event.target.checked }))} />{label}</label>;
          }
          return <label key={key} className="block text-xs font-medium">{label}<input type={property.type === 'number' || property.type === 'integer' ? 'number' : 'text'} value={String(values[key] ?? '')} onChange={(event) => setValues((current) => ({ ...current, [key]: property.type === 'number' || property.type === 'integer' ? Number(event.target.value) : event.target.value }))} className="mt-1 w-full rounded-md border border-gray-300 bg-white px-3 py-2 text-sm dark:border-white/[0.12] dark:bg-black/30" />{property.description && <span className="mt-1 block text-[11px] font-normal text-gray-500">{property.description}</span>}</label>;
        })}
        <div className="flex flex-wrap gap-2">
          <Button size="sm" disabled={disabled || !complete} onClick={() => onRespond(interaction.id, { action: 'accept', content: values })}>{t('workspaceChat.accept')}</Button>
          <Button size="sm" variant="secondary" disabled={disabled} onClick={() => onRespond(interaction.id, { action: 'decline', content: null })}>{t('workspaceChat.decline')}</Button>
          <Button size="sm" variant="danger" disabled={disabled} onClick={() => onRespond(interaction.id, { action: 'cancel', content: null })}>{t('common.cancel')}</Button>
        </div>
      </div>
    </InteractionShell>
  );
}

function ApprovalInteraction({ interaction, disabled, onRespond }: InteractionProps) {
  const { t } = useTranslation();
  const payload = asRecord(interaction.payload) || {};
  const decisions = interaction.allowed_decisions || [];
  return (
    <InteractionShell icon={<ShieldAlert size={16} />} title={t('workspaceChat.approvalRequired')} payload={interaction.payload}>
      {asString(payload.reason) && <p className="mb-2 whitespace-pre-wrap break-words text-sm">{asString(payload.reason)}</p>}
      {asString(payload.command) && <pre className="mb-3 max-w-full overflow-auto rounded-md bg-gray-950 p-3 text-xs text-gray-100">$ {asString(payload.command)}</pre>}
      <div className="flex flex-wrap gap-2">
        {decisions.map((decision, index) => {
          const label = decisionLabel(decision);
          const destructive = label === 'decline' || label === 'cancel' || label === 'deny';
          return <Button key={`${label}-${index}`} size="sm" variant={destructive ? 'danger' : index === 0 ? 'primary' : 'secondary'} disabled={disabled} onClick={() => onRespond(interaction.id, { decision })}>{t(`workspaceChat.decision.${label}`, { defaultValue: label })}</Button>;
        })}
        {decisions.length === 0 && <span className="text-xs text-amber-700 dark:text-amber-300">{t('workspaceChat.noAvailableDecision')}</span>}
      </div>
    </InteractionShell>
  );
}

function InteractionShell({ icon, title, payload, children }: { icon: React.ReactNode; title: string; payload: unknown; children: React.ReactNode }) {
  const { t } = useTranslation();
  return (
    <section className="rounded-md border border-amber-300 bg-amber-50 p-4 text-gray-800 dark:border-amber-700/80 dark:bg-amber-950/25 dark:text-gray-200">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-amber-800 dark:text-amber-200">{icon}<span className="min-w-0 break-words">{title}</span></div>
      {children}
      <div className="mt-3"><JsonDetails value={payload} label={t('workspaceChat.requestDetails')} /></div>
    </section>
  );
}

export function WorkspaceInteraction({ interaction, disabled, onRespond }: InteractionProps) {
  const { t } = useTranslation();
  switch (interaction.kind) {
    case 'item/tool/requestUserInput':
      return <UserInputInteraction interaction={interaction} disabled={disabled} onRespond={onRespond} />;
    case 'mcpServer/elicitation/request':
      return <MCPInteraction interaction={interaction} disabled={disabled} onRespond={onRespond} />;
    case 'item/permissions/requestApproval':
      return <PermissionInteraction interaction={interaction} disabled={disabled} onRespond={onRespond} />;
    case 'item/commandExecution/requestApproval':
    case 'item/fileChange/requestApproval':
      return <ApprovalInteraction interaction={interaction} disabled={disabled} onRespond={onRespond} />;
    default:
      return (
        <InteractionShell icon={<HelpCircle size={16} />} title={interaction.kind} payload={interaction.payload}>
          <span className="text-xs text-amber-700 dark:text-amber-300">{t('workspaceChat.noAvailableDecision')}</span>
        </InteractionShell>
      );
  }
}

export function WorkspaceInteractions({ interactions, disabled, onRespond }: { interactions: NativeInteraction[]; disabled: boolean; onRespond: InteractionProps['onRespond'] }) {
  if (interactions.length === 0) return null;
  return (
    <div className="space-y-3 py-4">
      {interactions.map((interaction) => <WorkspaceInteraction key={interaction.id} interaction={interaction} disabled={disabled} onRespond={onRespond} />)}
    </div>
  );
}
