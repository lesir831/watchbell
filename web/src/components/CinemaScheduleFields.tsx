import { Card, Col, Form, Input, Row, Select, Spin, Typography } from 'antd';
import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';

const { Text } = Typography;

export const cinemaDiscoveryConfigKeys = new Set([
  'cityId',
  'cityName',
  'cinemaId',
  'cinemaName',
  'movieId',
  'movieName'
]);

export default function CinemaScheduleFields({ proxyId }: { proxyId?: number | null }) {
  const form = Form.useFormInstance();
  const cityId = Form.useWatch<number>(['config', 'cityId'], form);
  const cityName = Form.useWatch<string>(['config', 'cityName'], form);
  const cinemaId = Form.useWatch<number>(['config', 'cinemaId'], form);
  const cinemaName = Form.useWatch<string>(['config', 'cinemaName'], form);
  const movieId = Form.useWatch<number>(['config', 'movieId'], form);
  const movieName = Form.useWatch<string>(['config', 'movieName'], form);
  const legacySelection = !cityId && cinemaId > 0 && movieId > 0;
  const [citySearch, setCitySearch] = useState('');
  const [cinemaSearch, setCinemaSearch] = useState('');
  const [movieSearch, setMovieSearch] = useState('');
  const debouncedCitySearch = useDebouncedValue(citySearch, 300);
  const debouncedCinemaSearch = useDebouncedValue(cinemaSearch, 300);
  const debouncedMovieSearch = useDebouncedValue(movieSearch, 300);

  const cities = useQuery({
    queryKey: ['cinemaDiscovery', 'cities', debouncedCitySearch.trim(), proxyId ?? null],
    queryFn: ({ signal }) => api.searchCinemaCities(debouncedCitySearch.trim(), proxyId, signal),
    staleTime: 5 * 60_000
  });
  const cinemas = useQuery({
    queryKey: ['cinemaDiscovery', 'cinemas', cityId, debouncedCinemaSearch.trim(), proxyId ?? null],
    queryFn: ({ signal }) => api.searchCinemas(cityId, debouncedCinemaSearch.trim(), proxyId, signal),
    enabled: cityId > 0 && debouncedCinemaSearch.trim().length > 0,
    staleTime: 5 * 60_000
  });
  const movies = useQuery({
    queryKey: ['cinemaDiscovery', 'movies', cityId, debouncedMovieSearch.trim(), proxyId ?? null],
    queryFn: ({ signal }) => api.searchCinemaMovies(cityId, debouncedMovieSearch.trim(), proxyId, signal),
    enabled: cityId > 0 && debouncedMovieSearch.trim().length > 0,
    staleTime: 5 * 60_000
  });

  const cityOptions = useMemo(() => withSelectedOption(
    (cities.data?.items ?? []).map((item) => ({
      value: item.id,
      name: item.name,
      label: item.pinyin ? `${item.name} · ${item.pinyin}` : item.name
    })),
    cityId,
    cityName,
    '城市'
  ), [cities.data?.items, cityId, cityName]);
  const cinemaOptions = useMemo(() => withSelectedOption(
    (cinemas.data?.items ?? []).map((item) => ({
      value: item.id,
      name: item.name,
      label: item.address ? `${item.name} · ${item.address}` : item.name
    })),
    cinemaId,
    cinemaName,
    '影院'
  ), [cinemas.data?.items, cinemaId, cinemaName]);
  const movieOptions = useMemo(() => withSelectedOption(
    (movies.data?.items ?? []).map((item) => ({
      value: item.id,
      name: item.name,
      label: [item.name, item.version, item.releaseDescription].filter(Boolean).join(' · ')
    })),
    movieId,
    movieName,
    '影片'
  ), [movies.data?.items, movieId, movieName]);

  const selectName = (path: 'cityName' | 'cinemaName' | 'movieName', option: unknown) => {
    const selected = Array.isArray(option) ? option[0] : option;
    const name = selected && typeof selected === 'object' && 'name' in selected ? String(selected.name ?? '') : '';
    form.setFieldValue(['config', path], name);
  };
  const clearVenueAndMovie = () => {
    form.setFieldValue(['config', 'cinemaId'], undefined);
    form.setFieldValue(['config', 'cinemaName'], '');
    form.setFieldValue(['config', 'movieId'], undefined);
    form.setFieldValue(['config', 'movieName'], '');
    setCinemaSearch('');
    setMovieSearch('');
  };

  return (
    <Card size="small" title="城市、影院与影片" className="form-intro cinema-discovery-card">
      <Row gutter={16}>
        <Col xs={24} sm={10}>
          <Form.Item name={['config', 'cityId']} label="城市" rules={legacySelection ? [] : [{ required: true, message: '请选择城市' }]} extra={legacySelection ? '这是未记录城市的旧配置；重新选择影院或影片前请先选择城市。' : '可按城市名或拼音搜索。'}>
            <Select
              allowClear
              showSearch
              filterOption={false}
              optionLabelProp="name"
              placeholder="搜索并选择城市"
              options={cityOptions}
              loading={cities.isFetching}
              notFoundContent={queryEmpty(cities.isFetching, cities.isError, '没有匹配的城市')}
              onSearch={setCitySearch}
              onChange={(_, option) => {
                selectName('cityName', option);
                clearVenueAndMovie();
              }}
            />
          </Form.Item>
        </Col>
        <Col xs={24} sm={14}>
          <Form.Item name={['config', 'cinemaId']} label="影院" rules={[{ required: true, message: '请选择影院' }]} extra={cityId ? '输入影院名称、商圈或地址查询。' : '请先选择城市。'}>
            <Select
              allowClear
              showSearch
              filterOption={false}
              optionLabelProp="name"
              disabled={!cityId}
              placeholder={cityId ? '输入关键字查询影院' : '请先选择城市'}
              options={cinemaOptions}
              loading={cinemas.isFetching}
              notFoundContent={queryEmpty(cinemas.isFetching, cinemas.isError, cinemaSearch.trim() ? '没有匹配的影院' : '输入关键字开始查询')}
              onSearch={setCinemaSearch}
              onChange={(_, option) => selectName('cinemaName', option)}
            />
          </Form.Item>
        </Col>
      </Row>
      <Form.Item name={['config', 'movieId']} label="影片" rules={[{ required: true, message: '请选择影片' }]} extra={cityId ? '在所选城市上映或待映影片中全局查询，不受影院当前排片限制。' : '请先选择城市。'}>
        <Select
          allowClear
          showSearch
          filterOption={false}
          optionLabelProp="name"
          disabled={!cityId}
          placeholder={cityId ? '输入片名查询影片' : '请先选择城市'}
          options={movieOptions}
          loading={movies.isFetching}
          notFoundContent={queryEmpty(movies.isFetching, movies.isError, movieSearch.trim() ? '没有匹配的影片' : '输入片名开始查询')}
          onSearch={setMovieSearch}
          onChange={(_, option) => selectName('movieName', option)}
        />
      </Form.Item>
      <Form.Item name={['config', 'cityName']} hidden><Input /></Form.Item>
      <Form.Item name={['config', 'cinemaName']} hidden><Input /></Form.Item>
      <Form.Item name={['config', 'movieName']} hidden rules={[{ required: true, message: '请选择影片' }]}><Input /></Form.Item>
      <Text type="secondary">{legacySelection ? '当前保留旧配置中的影院和影片；' : ''}查询由 WatchBell 服务端完成，当前监控配置的代理也会用于影院数据查询。</Text>
    </Card>
  );
}

interface DiscoveryOption {
  value: number;
  name: string;
  label: string;
}

function withSelectedOption(options: DiscoveryOption[], value: number | undefined, name: string | undefined, fallback: string) {
  if (!value || options.some((item) => item.value === value)) return options;
  return [{ value, name: name?.trim() || `${fallback} #${value}`, label: name?.trim() || `${fallback} #${value}` }, ...options];
}

function queryEmpty(loading: boolean, failed: boolean, empty: string) {
  if (loading) return <Spin size="small" />;
  if (failed) return <Text type="danger">查询失败，请稍后重试</Text>;
  return empty;
}

function useDebouncedValue<T>(value: T, delay: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [delay, value]);
  return debounced;
}
