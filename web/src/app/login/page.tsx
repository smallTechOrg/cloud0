import type { Metadata } from 'next';
import { LoginForm } from '@/components/LoginForm';

export const metadata: Metadata = {
  title: 'Sign in — cloud0',
};

export default function LoginPage() {
  return <LoginForm />;
}
