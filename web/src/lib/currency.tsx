import { createContext, useContext } from 'react';
import { DEFAULT_CURRENCY } from './format';

/** ISO 4217 code from `GET /config`; components read it with `useCurrency()`. */
export const CurrencyContext = createContext<string>(DEFAULT_CURRENCY);

export function useCurrency(): string {
  return useContext(CurrencyContext);
}
