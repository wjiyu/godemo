package sort

type IntArray []int

type Sorter interface {
	Len() int
	Less(i, j int) bool
	Swap(i, j int)
}

func (p IntArray) Len() int { return len(p) }

func (p IntArray) Less(i, j int) bool { return p[i] < p[j] }

func (p IntArray) Swap(i, j int) { p[i], p[j] = p[j], p[i] }

func Sort(data Sorter) {
	for pass := 1; pass < data.Len(); pass++ {
		for i := 0; i < data.Len()-pass; i++ {
			if data.Less(i+1, i) {
				data.Swap(i, i+1)
			}

		}
	}

}
