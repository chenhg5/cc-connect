import { Routes, Route, Navigate } from 'react-router-dom';
import { useAuthStore } from '@/store/auth';
import Layout from '@/components/Layout/Layout';
import Login from '@/pages/Login';
import Dashboard from '@/pages/Dashboard';
import ProjectList from '@/pages/Projects/ProjectList';
import ProjectDetail from '@/pages/Projects/ProjectDetail';
import ChatList from '@/pages/Chat/ChatList';
import ChatView from '@/pages/Chat/ChatView';
import CronList from '@/pages/Cron/CronList';
import SystemConfig from '@/pages/System/Config';
import ProviderList from '@/pages/Providers/ProviderList';
import SkillList from '@/pages/Skills/SkillList';
import CompanionLayout from '@/pages/Companion/CompanionLayout';
import CompanionChat from '@/pages/Companion/ChatView';
import CompanionDiary from '@/pages/Companion/DiaryView';
import CompanionTimeline from '@/pages/Companion/TimelineView';
import CompanionSaved from '@/pages/Companion/SavedView';
import CompanionSettings from '@/pages/Companion/SettingsView';

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated);
  if (!isAuthenticated) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

export default function App() {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated);

  return (
    <Routes>
      <Route path="/login" element={isAuthenticated ? <Navigate to="/" replace /> : <Login />} />
      <Route element={<ProtectedRoute><Layout /></ProtectedRoute>}>
        <Route index element={<Dashboard />} />
        <Route path="projects" element={<ProjectList />} />
        <Route path="projects/:name" element={<ProjectDetail />} />
        <Route path="providers" element={<ProviderList />} />
        <Route path="skills" element={<SkillList />} />
        <Route path="chat" element={<ChatList />} />
        <Route path="chat/:name" element={<ChatView />} />
        <Route path="cron" element={<CronList />} />
        <Route path="system" element={<SystemConfig />} />
      </Route>
      <Route path="/companion" element={<ProtectedRoute><CompanionLayout /></ProtectedRoute>}>
        <Route index element={<CompanionChat />} />
        <Route path="diary" element={<CompanionDiary />} />
        <Route path="timeline" element={<CompanionTimeline />} />
        <Route path="saved" element={<CompanionSaved />} />
        <Route path="settings" element={<CompanionSettings />} />
      </Route>
    </Routes>
  );
}
