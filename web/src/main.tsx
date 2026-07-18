import React from 'react';
import ReactDOM from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { App as AntApp, ConfigProvider } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import App from './App';
import './styles.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1
    }
  }
});

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <ConfigProvider
        locale={zhCN}
        theme={{
          token: {
            borderRadius: 10,
            borderRadiusLG: 16,
            colorPrimary: '#1779e1',
            colorLink: '#005cc2',
            colorBgLayout: '#fbfcfd',
            colorBgContainer: '#ffffff',
            colorText: '#0e1217',
            colorTextSecondary: '#6a6f76',
            colorBorder: '#e2e5e8',
            controlHeight: 44,
            fontFamily: "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'PingFang SC', system-ui, sans-serif"
          },
          components: {
            Button: {
              primaryShadow: '0 8px 22px rgba(23, 121, 225, 0.22)',
              defaultShadow: 'none'
            },
            Card: {
              boxShadow: 'none'
            },
            Menu: {
              darkItemBg: '#0e1217',
              darkSubMenuItemBg: '#0e1217',
              darkItemSelectedBg: '#242b35',
              itemBorderRadius: 8
            },
            Table: {
              headerBg: '#f6f7f8',
              headerColor: '#555b63',
              rowHoverBg: '#f8fafc'
            }
          }
        }}
      >
        <AntApp>
          <App />
        </AntApp>
      </ConfigProvider>
    </QueryClientProvider>
  </React.StrictMode>
);
